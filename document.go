package crdt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Feature is the current application-visible state of a GeoJSON feature.
// Geometry is nil for features without geometry.
type Feature struct {
	ID         ID              `json:"id"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
}

// featureState is the convergent state of one feature. Liveness, geometry
// identity, and every property are independent last-writer-wins registers,
// so operations commute regardless of delivery order.
type featureState struct {
	id ID

	// isBase marks features loaded from the document base; they are visible
	// without a create register and own the zero geometry generation.
	isBase bool

	createReg Stamp // newest insert_feature
	deleteReg Stamp // newest delete_feature

	// genID identifies the current geometry generation — the newest
	// insert_feature or set_geometry — and genStamp is its last-writer
	// stamp. The zero genID is the base generation.
	genID    OpRef
	genStamp Stamp
	geometry *geometryState // nil when the generation has no geometry

	// seenGens records every generation observed for this feature,
	// including superseded ones, so stale edits are classified
	// deterministically.
	seenGens map[OpRef]struct{}

	properties map[string]propertyState
}

// visible reports whether the feature currently exists: the newest of the
// create/delete registers wins, and base features exist until deleted.
func (f *featureState) visible() bool {
	if !f.deleteReg.isSet() {
		return f.isBase || f.createReg.isSet()
	}
	return f.createReg.newer(f.deleteReg)
}

// MergeResult reports what a merge did with each incoming operation. Merges
// never fail on operation content: operations waiting on dependencies are
// buffered and drained automatically, and operations that can never apply
// are quarantined. All classifications depend only on convergent state, so
// replicas agree on them.
type MergeResult struct {
	// Applied counts operations (including previously buffered ones) that
	// changed state during this merge.
	Applied int

	// Duplicates counts operations that were already known or compacted.
	Duplicates int

	// Superseded lists operations that lost a last-writer-wins comparison
	// or targeted a superseded geometry generation.
	Superseded []OpRef

	// Buffered lists operations from this merge still waiting on a missing
	// dependency (a feature generation, part, ring, or vertex) when the
	// merge returned.
	Buffered []OpRef

	// Quarantined lists operations that can never apply to this document
	// (for example vertex edits addressed to a Point, or removal of an
	// exterior ring).
	Quarantined []QuarantinedOp
}

// QuarantinedOp is one permanently inapplicable operation.
type QuarantinedOp struct {
	Ref    OpRef
	Reason string
}

// Document is a feature-collection CRDT combining feature lifecycle,
// property registers, and stable-ID geometry editing under one syncable
// object. All methods are safe for concurrent use.
type Document struct {
	mu sync.Mutex

	siteID   string
	clock    uint64 // Lamport timestamp
	localSeq uint64 // contiguous sequence number of local ops
	baseHash string
	options  documentOptions

	features map[ID]*featureState

	ops      []DocumentOp
	seen     map[OpRef]payloadHash
	frontier *frontierClock

	// compacted records, per site, the sequence number through which
	// operations were folded into the base by a snapshot. Operations at or
	// below the watermark are ignored: their effects are already part of
	// the base.
	compacted VectorClock

	// syncedThrough is the local sequence watermark returned by PendingOps
	// and advanced by MarkSynced.
	syncedThrough uint64

	// pendingGen buffers edit_geometry operations whose target geometry
	// generation has not arrived yet, keyed by the generation ref.
	pendingGen map[OpRef][]DocumentOp
}

// NewDocument creates an empty document for one replica site. An empty
// siteID gets a fresh random identity (see NewSiteID). All documents
// created empty share a base lineage and can merge with each other.
func NewDocument(siteID string, opts ...DocumentOption) *Document {
	options := documentOptions{topologyPolicy: AllowInvalidIntermediate}
	for _, opt := range opts {
		opt(&options)
	}
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		siteID = NewSiteID()
	}
	return &Document{
		siteID:     siteID,
		baseHash:   emptyBaseHash,
		options:    options,
		features:   make(map[ID]*featureState),
		seen:       make(map[OpRef]payloadHash),
		frontier:   newFrontierClock(),
		compacted:  make(VectorClock),
		pendingGen: make(map[OpRef][]DocumentOp),
	}
}

// NewDocumentFromFeatureCollection creates a document whose base state is a
// GeoJSON FeatureCollection. Loading a base adds no operations: replicas
// that load the same collection share a base lineage and exchange deltas.
func NewDocumentFromFeatureCollection(siteID string, data json.RawMessage, opts ...DocumentOption) (*Document, error) {
	features, err := parseFeatureCollection(data)
	if err != nil {
		return nil, err
	}
	doc := NewDocument(siteID, opts...)
	for _, feature := range features {
		state := &featureState{
			id:         feature.id,
			isBase:     true,
			seenGens:   map[OpRef]struct{}{{}: {}},
			properties: make(map[string]propertyState, len(feature.properties)),
		}
		if feature.geometry != nil {
			state.geometry, err = newGeometryState(feature.geometry)
			if err != nil {
				return nil, fmt.Errorf("feature %s: %w", feature.id, err)
			}
		}
		for key, value := range feature.properties {
			state.properties[key] = propertyState{value: cloneRawMessage(value)}
		}
		doc.features[feature.id] = state
	}
	doc.baseHash = computeBaseHash(features)
	return doc, nil
}

// SiteID returns the local replica site ID.
func (d *Document) SiteID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.siteID
}

// Clock returns the current Lamport clock.
func (d *Document) Clock() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clock
}

// BaseHash identifies the document's base lineage. Only documents with equal
// base hashes can merge.
func (d *Document) BaseHash() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.baseHash
}

// VectorClock returns, per site, the sequence number through which this
// document contiguously knows that site's operations (including operations
// folded by compaction). Operations staged beyond a delivery gap are
// excluded, so deltas computed against this clock always re-request the gap.
func (d *Document) VectorClock() VectorClock {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.knowledgeLocked()
}

func (d *Document) knowledgeLocked() VectorClock {
	return mergeVectorClocks(d.compacted, d.frontier.frontier)
}

// Ops returns a copy of the document operation log.
func (d *Document) Ops() []DocumentOp {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneDocumentOps(d.ops)
}

// PendingOps returns local operations not yet marked synced, along with a
// watermark to pass to MarkSynced once the operations are durably sent.
// Operations applied after this call are not covered by the watermark, so
// the PendingOps/MarkSynced pair is race-free.
func (d *Document) PendingOps() ([]DocumentOp, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	watermark := d.syncedThrough
	var pending []DocumentOp
	for _, op := range d.ops {
		if op.SiteID != d.siteID || op.Seq <= d.syncedThrough {
			continue
		}
		pending = append(pending, op.normalize().stripEmbeddedIdentity())
		if op.Seq > watermark {
			watermark = op.Seq
		}
	}
	return pending, watermark
}

// MarkSynced records that local operations up to the watermark returned by
// PendingOps have been sent.
func (d *Document) MarkSynced(watermark uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if watermark > d.syncedThrough {
		d.syncedThrough = watermark
	}
}

// --- Local commands ---

// Apply validates and applies a local command, assigning it the next Lamport
// timestamp and sequence number. Commands are validated strictly against
// current state; remote operations merged later are never validated this way
// (they buffer or quarantine instead).
func (d *Document) Apply(command any) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	nextClock := d.clock + 1
	nextSeq := d.localSeq + 1
	if nextClock >= MaxTimestamp || nextSeq >= MaxTimestamp {
		return fmt.Errorf("%w: clock exhausted", ErrInvalidCommand)
	}
	op, err := d.buildLocalOp(nextSeq, nextClock, command)
	if err != nil {
		return err
	}

	result, _ := d.applyOpLocked(op)
	if result.outcome != outcomeApplied {
		// Local commands are pre-validated; any other outcome is a bug.
		return fmt.Errorf("%w: local command did not apply: %s", ErrInvalidCommand, result.reason)
	}
	d.recordOp(op)
	d.clock = nextClock
	d.localSeq = nextSeq
	return nil
}

func (d *Document) buildLocalOp(seq, timestamp uint64, command any) (DocumentOp, error) {
	envelope := DocumentOp{SiteID: d.siteID, Seq: seq, Timestamp: timestamp}

	switch cmd := command.(type) {
	case InsertFeature:
		if strings.TrimSpace(string(cmd.FeatureID)) == "" {
			return DocumentOp{}, fmt.Errorf("%w: feature_id is required", ErrInvalidCommand)
		}
		geometry, err := validateCommandGeometry(cmd.Geometry)
		if err != nil {
			return DocumentOp{}, err
		}
		properties, err := encodeProperties(cmd.Properties)
		if err != nil {
			return DocumentOp{}, err
		}
		envelope.Type = OpInsertFeature
		envelope.FeatureID = cmd.FeatureID
		envelope.Geometry = geometry
		envelope.Properties = properties
		return envelope, nil
	case DeleteFeature:
		if _, err := d.visibleFeature(cmd.FeatureID); err != nil {
			return DocumentOp{}, err
		}
		envelope.Type = OpDeleteFeature
		envelope.FeatureID = cmd.FeatureID
		return envelope, nil
	case SetGeometry:
		if _, err := d.visibleFeature(cmd.FeatureID); err != nil {
			return DocumentOp{}, err
		}
		geometry, err := validateCommandGeometry(cmd.Geometry)
		if err != nil {
			return DocumentOp{}, err
		}
		envelope.Type = OpSetGeometry
		envelope.FeatureID = cmd.FeatureID
		envelope.Geometry = geometry
		return envelope, nil
	case SetProperty:
		if strings.TrimSpace(cmd.Key) == "" {
			return DocumentOp{}, fmt.Errorf("%w: property key is required", ErrInvalidCommand)
		}
		if _, err := d.visibleFeature(cmd.FeatureID); err != nil {
			return DocumentOp{}, err
		}
		value, err := encodePropertyValue(cmd.Value)
		if err != nil {
			return DocumentOp{}, err
		}
		envelope.Type = OpSetProperty
		envelope.FeatureID = cmd.FeatureID
		envelope.PropertyKey = cmd.Key
		envelope.PropertyValue = value
		return envelope, nil
	case DeleteProperty:
		if strings.TrimSpace(cmd.Key) == "" {
			return DocumentOp{}, fmt.Errorf("%w: property key is required", ErrInvalidCommand)
		}
		if _, err := d.visibleFeature(cmd.FeatureID); err != nil {
			return DocumentOp{}, err
		}
		envelope.Type = OpDeleteProperty
		envelope.FeatureID = cmd.FeatureID
		envelope.PropertyKey = cmd.Key
		return envelope, nil
	}

	featureID, geometryOp, ok := geometryOpForCommand(command)
	if !ok {
		return DocumentOp{}, fmt.Errorf("%w: unsupported document command %T", ErrInvalidCommand, command)
	}
	feature, err := d.visibleFeature(featureID)
	if err != nil {
		return DocumentOp{}, err
	}
	if feature.geometry == nil {
		return DocumentOp{}, fmt.Errorf("%w: feature %q has no geometry", ErrInvalidCommand, featureID)
	}
	geometryOp.SiteID = d.siteID
	geometryOp.Seq = seq
	geometryOp.Timestamp = timestamp
	geometryOp = geometryOp.truncateCoords(feature.geometry.dims)
	geometryOp, err = geometryOp.deriveCreatedIDs()
	if err != nil {
		return DocumentOp{}, fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	if err := feature.geometry.validateLocalOp(geometryOp); err != nil {
		return DocumentOp{}, err
	}
	envelope.Type = OpEditGeometry
	envelope.FeatureID = featureID
	envelope.GeometryOp = &geometryOp
	if feature.genID.isSet() {
		gen := feature.genID
		envelope.Gen = &gen
	}
	return envelope, nil
}

func validateCommandGeometry(geometry json.RawMessage) (json.RawMessage, error) {
	if isNullGeometry(geometry) {
		return nil, nil
	}
	if _, err := newGeometryState(geometry); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	return cloneRawMessage(geometry), nil
}

func (d *Document) visibleFeature(id ID) (*featureState, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, fmt.Errorf("%w: feature_id is required", ErrInvalidCommand)
	}
	feature, ok := d.features[id]
	if !ok || !feature.visible() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownFeature, id)
	}
	return feature, nil
}

// --- Merging ---

// Merge merges all operations from another document. Both documents must
// share a base lineage, and the remote's compaction watermark must be
// covered by this document's knowledge (otherwise operations folded into the
// remote's base would be unrecoverable here; load a snapshot instead).
func (d *Document) Merge(remote *Document) (MergeResult, error) {
	if d == remote {
		return MergeResult{}, nil
	}
	remote.mu.Lock()
	remoteOps := cloneDocumentOps(remote.ops)
	remoteClock := remote.clock
	remoteBaseHash := remote.baseHash
	remoteCompacted := cloneVectorClock(remote.compacted)
	remote.mu.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	if remoteBaseHash != d.baseHash {
		return MergeResult{}, ErrBaseMismatch
	}
	if !remoteCompacted.coveredBy(d.knowledgeLocked()) {
		return MergeResult{}, ErrCompactionGap
	}

	result, err := d.mergeOpsLocked(remoteOps)
	if err != nil {
		return MergeResult{}, err
	}
	d.compacted = mergeVectorClocks(d.compacted, remoteCompacted)
	if remoteClock > d.clock {
		d.clock = remoteClock
	}
	return result, nil
}

// MergeOps merges wire operations that share this document's base lineage.
// Transports that carry lineage metadata should prefer MergeDelta, which
// verifies it.
func (d *Document) MergeOps(ops []DocumentOp) (MergeResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mergeOpsLocked(ops)
}

func (d *Document) mergeOpsLocked(incoming []DocumentOp) (MergeResult, error) {
	// Validate every envelope before touching state: envelope validity is
	// context-free, so a failure is a protocol error, and rejecting the
	// batch up front keeps merges atomic.
	normalized := make([]DocumentOp, 0, len(incoming))
	hashes := make(map[OpRef]payloadHash, len(incoming))
	nextClock := d.clock
	for _, op := range incoming {
		op = op.normalize()
		if op.GeometryOp != nil {
			geometryOp, err := op.GeometryOp.deriveCreatedIDs()
			if err != nil {
				return MergeResult{}, err
			}
			op.GeometryOp = &geometryOp
		}
		if err := op.validateEnvelope(); err != nil {
			return MergeResult{}, err
		}
		if op.SiteID == d.siteID && op.Seq > d.localSeq {
			// An op claiming this replica's identity beyond its own history
			// means the site ID is in use elsewhere (for example after a
			// snapshot restore); merging it would corrupt local numbering.
			return MergeResult{}, fmt.Errorf("%w: received op %s:%d beyond local history %d; site identity reused",
				ErrInvalidOp, op.SiteID, op.Seq, d.localSeq)
		}
		if op.Timestamp > nextClock {
			nextClock = op.Timestamp
		}
		hash, err := hashDocumentOp(op)
		if err != nil {
			return MergeResult{}, err
		}
		ref := op.ref()
		if known, ok := d.seen[ref]; ok && known != hash {
			return MergeResult{}, fmt.Errorf("%w: %s", ErrIdentityCollision, ref)
		}
		if known, ok := hashes[ref]; ok && known != hash {
			return MergeResult{}, fmt.Errorf("%w: %s", ErrIdentityCollision, ref)
		}
		hashes[ref] = hash
		normalized = append(normalized, op)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].stamp().less(normalized[j].stamp())
	})

	tally := newMergeTally()
	for _, op := range normalized {
		ref := op.ref()
		if op.Seq <= d.compacted[op.SiteID] {
			tally.duplicate()
			continue
		}
		if _, dup := d.seen[ref]; dup {
			tally.duplicate()
			continue
		}
		res, drained := d.applyOpLocked(op)
		// Every non-duplicate op joins the log, whatever its outcome: it is
		// part of history, and peers may still need it.
		d.recordOp(op)
		tally.record(opOutcome{ref: ref, outcome: res.outcome, reason: res.reason})
		for _, o := range drained {
			tally.record(o)
		}
	}

	if nextClock > d.clock {
		d.clock = nextClock
	}
	return tally.finish(), nil
}

// mergeTally accumulates a MergeResult across a batch. Buffered refs that
// later drain within the same batch are reclassified by their final outcome.
type mergeTally struct {
	result        MergeResult
	stillBuffered map[OpRef]struct{}
	bufferedOrder []OpRef
}

func newMergeTally() *mergeTally {
	return &mergeTally{stillBuffered: make(map[OpRef]struct{})}
}

func (t *mergeTally) duplicate() {
	t.result.Duplicates++
}

func (t *mergeTally) record(o opOutcome) {
	switch o.outcome {
	case outcomeApplied:
		t.result.Applied++
		delete(t.stillBuffered, o.ref)
	case outcomeDuplicate:
		t.result.Duplicates++
	case outcomeSuperseded:
		t.result.Superseded = append(t.result.Superseded, o.ref)
		delete(t.stillBuffered, o.ref)
	case outcomeBuffered:
		if _, tracked := t.stillBuffered[o.ref]; !tracked {
			t.stillBuffered[o.ref] = struct{}{}
			t.bufferedOrder = append(t.bufferedOrder, o.ref)
		}
	case outcomeQuarantined:
		t.result.Quarantined = append(t.result.Quarantined, QuarantinedOp{Ref: o.ref, Reason: o.reason})
		delete(t.stillBuffered, o.ref)
	}
}

func (t *mergeTally) finish() MergeResult {
	for _, ref := range t.bufferedOrder {
		if _, waiting := t.stillBuffered[ref]; waiting {
			t.result.Buffered = append(t.result.Buffered, ref)
		}
	}
	return t.result
}

func (d *Document) recordOp(op DocumentOp) {
	d.ops = append(d.ops, op)
	hash, err := hashDocumentOp(op)
	if err != nil {
		panic(fmt.Sprintf("crdt: hash validated operation: %v", err))
	}
	d.seen[op.ref()] = hash
	d.frontier.observe(op.SiteID, op.Seq)
}

// --- Operation application ---

// feature returns the state for an ID, creating an invisible shell to hold
// registers and buffers for features that have not been inserted yet.
func (d *Document) feature(id ID) *featureState {
	if state, ok := d.features[id]; ok {
		return state
	}
	state := &featureState{
		id:         id,
		seenGens:   make(map[OpRef]struct{}),
		properties: make(map[string]propertyState),
	}
	d.features[id] = state
	return state
}

func (d *Document) applyOpLocked(op DocumentOp) (applyResult, []opOutcome) {
	switch op.Type {
	case OpInsertFeature:
		return d.applyInsertFeature(op)
	case OpDeleteFeature:
		return d.applyDeleteFeature(op), nil
	case OpSetGeometry:
		return d.applySetGeometry(op)
	case OpSetProperty:
		return d.applyWriteProperty(op, false), nil
	case OpDeleteProperty:
		return d.applyWriteProperty(op, true), nil
	case OpEditGeometry:
		return d.applyEditGeometry(op)
	default:
		// Unknown types are rejected by envelope validation.
		return quarantined("unknown document operation type %q", op.Type), nil
	}
}

func (d *Document) applyInsertFeature(op DocumentOp) (applyResult, []opOutcome) {
	engine, fail := parseOpGeometry(op.Geometry)
	if fail.reason != "" {
		return fail, nil
	}
	feature := d.feature(op.FeatureID)
	stamp := op.stamp()

	for _, key := range sortedPropertyKeys(op.Properties) {
		writePropertyRegister(feature, key, op.Properties[key], false, stamp)
	}
	if stamp.newer(feature.createReg) {
		feature.createReg = stamp
	}
	outcomes := d.adoptGeneration(feature, op.ref(), stamp, engine)
	return applied(), outcomes
}

func (d *Document) applyDeleteFeature(op DocumentOp) applyResult {
	feature := d.feature(op.FeatureID)
	if !op.stamp().newer(feature.deleteReg) {
		return superseded()
	}
	feature.deleteReg = op.stamp()
	return applied()
}

func (d *Document) applySetGeometry(op DocumentOp) (applyResult, []opOutcome) {
	engine, fail := parseOpGeometry(op.Geometry)
	if fail.reason != "" {
		return fail, nil
	}
	feature := d.feature(op.FeatureID)
	if !op.stamp().newer(feature.genStamp) {
		// Record the generation even though it lost, so its edits are
		// classified as superseded rather than buffered forever.
		feature.seenGens[op.ref()] = struct{}{}
		delete(d.pendingGen, op.ref())
		return superseded(), nil
	}
	outcomes := d.adoptGeneration(feature, op.ref(), op.stamp(), engine)
	return applied(), outcomes
}

// adoptGeneration installs a geometry generation if it wins the geometry
// register, then drains edits that were waiting for it.
func (d *Document) adoptGeneration(feature *featureState, ref OpRef, stamp Stamp, engine *geometryState) []opOutcome {
	feature.seenGens[ref] = struct{}{}
	waiting := d.pendingGen[ref]
	delete(d.pendingGen, ref)

	if !stamp.newer(feature.genStamp) {
		// Superseded on arrival: buffered edits for this generation can
		// never become visible.
		var outcomes []opOutcome
		for _, op := range waiting {
			outcomes = append(outcomes, opOutcome{ref: op.ref(), outcome: outcomeSuperseded})
		}
		return outcomes
	}

	feature.geometry = engine
	feature.genID = ref
	feature.genStamp = stamp

	sort.SliceStable(waiting, func(i, j int) bool {
		return waiting[i].stamp().less(waiting[j].stamp())
	})
	var outcomes []opOutcome
	for _, op := range waiting {
		if op.FeatureID != feature.id {
			outcomes = append(outcomes, opOutcome{
				ref:     op.ref(),
				outcome: outcomeQuarantined,
				reason:  fmt.Sprintf("generation %s belongs to feature %q, not %q", ref, feature.id, op.FeatureID),
			})
			continue
		}
		res, drained := d.applyEditToEngine(feature, op)
		outcomes = append(outcomes, opOutcome{ref: op.ref(), outcome: res.outcome, reason: res.reason})
		outcomes = append(outcomes, drained...)
	}
	return outcomes
}

func (d *Document) applyWriteProperty(op DocumentOp, deleted bool) applyResult {
	feature := d.feature(op.FeatureID)
	if writePropertyRegister(feature, op.PropertyKey, op.PropertyValue, deleted, op.stamp()) {
		return applied()
	}
	return superseded()
}

func writePropertyRegister(feature *featureState, key string, value json.RawMessage, deleted bool, stamp Stamp) bool {
	// Base properties carry the zero stamp and lose to any operation.
	current := feature.properties[key]
	if current.ref.isSet() && !stamp.newer(current.ref) {
		return false
	}
	feature.properties[key] = propertyState{
		value:   cloneRawMessage(value),
		deleted: deleted,
		ref:     stamp,
	}
	return true
}

func (d *Document) applyEditGeometry(op DocumentOp) (applyResult, []opOutcome) {
	feature := d.feature(op.FeatureID)
	gen := op.genRef()
	_, genSeen := feature.seenGens[gen]

	if gen == feature.genID && genSeen {
		return d.applyEditToEngine(feature, op)
	}
	if genSeen {
		// The generation exists but lost the geometry register.
		return superseded(), nil
	}
	if !gen.isSet() {
		// The zero generation exists only for base features; shared base
		// hashes guarantee replicas agree on which features those are.
		return quarantined("feature %q has no base generation", op.FeatureID), nil
	}
	if gen.Seq <= d.compacted[gen.SiteID] {
		// The defining op was compacted away without producing this
		// generation; it can never arrive.
		return quarantined("generation %s was compacted without defining feature %q", gen, op.FeatureID), nil
	}
	if _, opSeen := d.seen[gen]; opSeen {
		// The referenced op was received but did not define a generation
		// for this feature.
		return quarantined("operation %s does not define a geometry generation for feature %q", gen, op.FeatureID), nil
	}
	d.pendingGen[gen] = append(d.pendingGen[gen], op)
	return buffered(depKey{}), nil
}

func (d *Document) applyEditToEngine(feature *featureState, op DocumentOp) (applyResult, []opOutcome) {
	if feature.geometry == nil {
		return quarantined("geometry generation of feature %q has no geometry", op.FeatureID), nil
	}
	return feature.geometry.apply(*op.GeometryOp)
}

func parseOpGeometry(raw json.RawMessage) (*geometryState, applyResult) {
	if isNullGeometry(raw) {
		return nil, applyResult{}
	}
	engine, err := newGeometryState(raw)
	if err != nil {
		return nil, quarantined("geometry: %v", err)
	}
	return engine, applyResult{}
}

// --- Reads ---

// Feature returns one visible feature by ID.
func (d *Document) Feature(id ID) (Feature, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.features[id]
	if !ok || !state.visible() {
		return Feature{}, false
	}
	return d.featureView(state), true
}

// Features returns the visible features in deterministic ID order.
func (d *Document) Features() []Feature {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.featuresLocked()
}

func (d *Document) featuresLocked() []Feature {
	ids := make([]ID, 0, len(d.features))
	for id, state := range d.features {
		if state.visible() {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	features := make([]Feature, 0, len(ids))
	for _, id := range ids {
		features = append(features, d.featureView(d.features[id]))
	}
	return features
}

func (d *Document) featureView(state *featureState) Feature {
	return Feature{
		ID:         state.id,
		Geometry:   d.geometryView(state),
		Properties: decodeProperties(state.properties),
	}
}

// geometryView renders a feature geometry, applying the configured polygon
// repairs as a deterministic view transform.
func (d *Document) geometryView(state *featureState) json.RawMessage {
	if state.geometry == nil {
		return nil
	}
	raw := state.geometry.geoJSON()
	if d.options.polygonRepair == RepairNone {
		return raw
	}
	return repairGeometryView(raw, d.options.polygonRepair)
}

// GeometryInfo returns the visible geometry structure of a feature with the
// stable part, ring, and vertex IDs used to address edits. It returns false
// for unknown, deleted, or geometry-less features.
func (d *Document) GeometryInfo(id ID) ([]PartInfo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.features[id]
	if !ok || !state.visible() || state.geometry == nil {
		return nil, false
	}
	return state.geometry.info(), true
}

// VertexIDAt resolves the stable ID of the index-th visible vertex of a
// feature ring.
func (d *Document) VertexIDAt(id ID, partID, ringID string, index int) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, err := d.visibleFeature(id)
	if err != nil {
		return "", err
	}
	if state.geometry == nil {
		return "", fmt.Errorf("%w: feature %q has no geometry", ErrInvalidCommand, id)
	}
	return state.geometry.vertexIDAt(partID, ringID, index)
}

// FeatureCollection returns the current visible state as a GeoJSON
// FeatureCollection value. It does not run topology validation; use
// FeatureCollectionJSON for policy-enforced exports.
func (d *Document) FeatureCollection() GeoJSONFeatureCollection {
	return featureCollectionFromFeatures(d.Features())
}

// FeatureCollectionJSON returns the current visible state as encoded
// GeoJSON, enforcing the document's topology policy.
func (d *Document) FeatureCollectionJSON() (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.options.topologyPolicy == ValidateOnExport {
		if err := d.validateTopologyLocked(); err != nil {
			return nil, err
		}
	}
	collection := featureCollectionFromFeatures(d.featuresLocked())
	data, err := json.Marshal(collection)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func (d *Document) validateTopologyLocked() error {
	for id, state := range d.features {
		if !state.visible() || state.geometry == nil {
			continue
		}
		if err := validateGeometryView(d.geometryView(state)); err != nil {
			return fmt.Errorf("feature %s: %w", id, err)
		}
	}
	return nil
}

func cloneDocumentOps(ops []DocumentOp) []DocumentOp {
	if len(ops) == 0 {
		return nil
	}
	result := make([]DocumentOp, len(ops))
	for i, op := range ops {
		result[i] = op.normalize()
	}
	return result
}
