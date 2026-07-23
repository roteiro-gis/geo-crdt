package crdt

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ProtocolVersion is the wire protocol version for Delta and Snapshot.
const ProtocolVersion = 3

// Delta is a versioned, transport-neutral operation batch. It carries the
// sender's base lineage and compaction watermark so receivers can verify
// compatibility before applying anything.
type Delta struct {
	Version     int          `json:"version"`
	SiteID      string       `json:"site_id"`
	BaseHash    string       `json:"base_hash"`
	Clock       uint64       `json:"clock"`
	VectorClock VectorClock  `json:"vector_clock"`
	Compacted   VectorClock  `json:"compacted,omitempty"`
	Ops         []DocumentOp `json:"ops"`
}

// DeltaSince returns the operations this document holds beyond the given
// vector clock (as returned by the peer's Document.VectorClock). A nil clock
// returns every operation in the log.
func (d *Document) DeltaSince(clock VectorClock) Delta {
	d.mu.Lock()
	defer d.mu.Unlock()

	var ops []DocumentOp
	for _, op := range d.ops {
		if op.Seq > clock[op.SiteID] {
			ops = append(ops, op.normalize().stripEmbeddedIdentity())
		}
	}
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].SiteID != ops[j].SiteID {
			return ops[i].SiteID < ops[j].SiteID
		}
		return ops[i].Seq < ops[j].Seq
	})

	return Delta{
		Version:     ProtocolVersion,
		SiteID:      d.siteID,
		BaseHash:    d.baseHash,
		Clock:       d.clock,
		VectorClock: d.knowledgeLocked(),
		Compacted:   cloneVectorClock(d.compacted),
		Ops:         ops,
	}
}

// MergeDelta verifies a delta's protocol version, base lineage, and
// compaction watermark, then merges its operations. Content-level issues
// never fail the merge; they are reported in the MergeResult.
func (d *Document) MergeDelta(delta Delta) (MergeResult, error) {
	if delta.Version != ProtocolVersion {
		return MergeResult{}, fmt.Errorf("%w: delta version %d", ErrUnsupportedVersion, delta.Version)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if delta.BaseHash != d.baseHash {
		return MergeResult{}, ErrBaseMismatch
	}
	if !delta.Compacted.coveredBy(d.knowledgeLocked()) {
		return MergeResult{}, ErrCompactionGap
	}

	result, err := d.mergeOpsLocked(delta.Ops)
	if err != nil {
		return MergeResult{}, err
	}
	d.compacted = mergeVectorClocks(d.compacted, delta.Compacted)
	if delta.Clock > d.clock {
		d.clock = delta.Clock
	}
	return result, nil
}

// --- Snapshots ---

// Snapshot is a complete, compact serialization of a document's convergent
// state: every register, tombstone, and buffered operation. Loading a
// snapshot produces a full replica that continues to merge deltas with its
// lineage; operations covered by the snapshot's vector clock are folded in
// and no longer exchanged.
//
// Snapshots are state transfer, not export: geometries are stored losslessly
// and no topology validation or repair is applied. Use
// FeatureCollectionJSON for policy-enforced GeoJSON exports.
type Snapshot struct {
	Version  int    `json:"version"`
	ID       string `json:"id,omitempty"`
	SiteID   string `json:"site_id"`
	BaseHash string `json:"base_hash"`
	Clock    uint64 `json:"clock"`

	// VectorClock is the contiguous-knowledge watermark: operations at or
	// below it are folded into this snapshot and must not be re-applied.
	VectorClock VectorClock `json:"vector_clock"`

	// Applied records the highest sequence number per site whose effects
	// are folded into this snapshot, including operations received beyond a
	// delivery gap. Restoring a replica under its old site ID resumes local
	// numbering above this to avoid identity collisions.
	Applied VectorClock `json:"applied,omitempty"`

	// SyncedThrough is the acknowledged local outbox watermark.
	SyncedThrough uint64 `json:"synced_through,omitempty"`

	// OutboxOps contains local operations beyond SyncedThrough. Their
	// effects are already represented by Features, but their payloads must
	// survive a crash so PendingOps can publish them after restore.
	OutboxOps []DocumentOp `json:"outbox_ops,omitempty"`

	Features []FeatureSnapshot `json:"features"`

	// RetainedOps contains every operation beyond its actor's contiguous
	// VectorClock frontier. Their effects are represented by Features, but
	// the operations remain syncable until the preceding gaps are filled.
	// This includes applied, superseded, quarantined, and buffered dots.
	RetainedOps []DocumentOp `json:"retained_ops,omitempty"`

	// PendingOps are operations that were received but still waiting on
	// missing dependencies when the snapshot was taken. They are listed
	// separately so restore can rebuild dependency buffers without replaying
	// already-folded effects.
	PendingOps []DocumentOp `json:"pending_ops,omitempty"`
}

// FeatureSnapshot is the full register state of one feature, including
// tombstoned features.
type FeatureSnapshot struct {
	ID         ID                          `json:"id"`
	Base       bool                        `json:"base,omitempty"`
	CreateReg  *Stamp                      `json:"create_reg,omitempty"`
	DeleteReg  *Stamp                      `json:"delete_reg,omitempty"`
	GenID      *OpRef                      `json:"gen_id,omitempty"`
	GenStamp   *Stamp                      `json:"gen_stamp,omitempty"`
	SeenGens   []OpRef                     `json:"seen_gens,omitempty"`
	Properties map[string]PropertySnapshot `json:"properties,omitempty"`
	Geometry   *GeometrySnapshot           `json:"geometry,omitempty"`
}

// PropertySnapshot is one property register, including tombstones.
type PropertySnapshot struct {
	Value   json.RawMessage `json:"value,omitempty"`
	Deleted bool            `json:"deleted,omitempty"`
	Reg     *Stamp          `json:"reg,omitempty"`
}

// GeometrySnapshot is the lossless state of one geometry: the full vertex
// trees with tombstones, ordering keys, and move registers.
type GeometrySnapshot struct {
	Type  GeometryType   `json:"type"`
	Dims  int            `json:"dims"`
	Parts []PartSnapshot `json:"parts"`
}

// PartSnapshot is one geometry part, including tombstoned parts.
type PartSnapshot struct {
	ID      string         `json:"id"`
	Key     KeySnapshot    `json:"key"`
	Type    GeometryType   `json:"part_type"`
	Deleted bool           `json:"deleted,omitempty"`
	Rings   []RingSnapshot `json:"rings"`
}

// RingSnapshot is one ring, including tombstoned rings.
type RingSnapshot struct {
	ID       string           `json:"id"`
	Key      KeySnapshot      `json:"key"`
	Exterior bool             `json:"exterior,omitempty"`
	Deleted  bool             `json:"deleted,omitempty"`
	Vertices []VertexSnapshot `json:"vertices"`
}

// VertexSnapshot is one vertex in document order (parents always precede
// children), including tombstoned vertices.
type VertexSnapshot struct {
	ID      string      `json:"id"`
	Parent  string      `json:"parent,omitempty"`
	Key     KeySnapshot `json:"key"`
	Coord   []float64   `json:"coord"`
	Deleted bool        `json:"deleted,omitempty"`
	MoveReg *Stamp      `json:"move_reg,omitempty"`
}

// KeySnapshot encodes a sibling-ordering key: either an initial position or
// the creating operation's stamp.
type KeySnapshot struct {
	Init  *int   `json:"init,omitempty"`
	Stamp *Stamp `json:"stamp,omitempty"`
}

// Snapshot returns a complete state snapshot of the document.
func (d *Document) Snapshot(id string) (Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	snapshot := Snapshot{
		Version:       ProtocolVersion,
		ID:            id,
		SiteID:        d.siteID,
		BaseHash:      d.baseHash,
		Clock:         d.clock,
		VectorClock:   d.knowledgeLocked(),
		Applied:       d.appliedClockLocked(),
		SyncedThrough: d.syncedThrough,
	}
	for _, op := range d.ops {
		if op.SiteID == d.siteID && op.Seq > d.syncedThrough {
			snapshot.OutboxOps = append(snapshot.OutboxOps, op.normalize().stripEmbeddedIdentity())
		}
	}
	for _, op := range d.ops {
		if op.Seq > snapshot.VectorClock[op.SiteID] {
			snapshot.RetainedOps = append(snapshot.RetainedOps, op.normalize().stripEmbeddedIdentity())
		}
	}
	sort.SliceStable(snapshot.RetainedOps, func(i, j int) bool {
		if snapshot.RetainedOps[i].SiteID != snapshot.RetainedOps[j].SiteID {
			return snapshot.RetainedOps[i].SiteID < snapshot.RetainedOps[j].SiteID
		}
		return snapshot.RetainedOps[i].Seq < snapshot.RetainedOps[j].Seq
	})

	ids := make([]ID, 0, len(d.features))
	for featureID := range d.features {
		ids = append(ids, featureID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var pending []DocumentOp
	for _, featureID := range ids {
		state := d.features[featureID]
		fs, empty := snapshotFeature(state)
		if !empty {
			snapshot.Features = append(snapshot.Features, fs)
		}
		if state.geometry != nil {
			// Operations buffered inside the engine re-enter as regular
			// edit_geometry ops on restore.
			gen := state.genID
			for _, op := range state.geometry.pendingOps() {
				geometryOp := op
				pendingOp := DocumentOp{
					Type:       OpEditGeometry,
					SiteID:     op.SiteID,
					Seq:        op.Seq,
					Timestamp:  op.Timestamp,
					FeatureID:  featureID,
					GeometryOp: &geometryOp,
				}
				if gen.isSet() {
					genCopy := gen
					pendingOp.Gen = &genCopy
				}
				pending = append(pending, pendingOp)
			}
		}
	}
	for _, waiting := range d.pendingGen {
		pending = append(pending, cloneDocumentOps(waiting)...)
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].SiteID != pending[j].SiteID {
			return pending[i].SiteID < pending[j].SiteID
		}
		return pending[i].Seq < pending[j].Seq
	})
	for i := range pending {
		pending[i] = pending[i].stripEmbeddedIdentity()
	}
	snapshot.PendingOps = pending
	return snapshot, nil
}

// appliedClockLocked returns the highest applied sequence per site,
// including operations staged beyond delivery gaps.
func (d *Document) appliedClockLocked() VectorClock {
	applied := cloneVectorClock(d.compacted)
	for ref := range d.seen {
		if ref.Seq > applied[ref.SiteID] {
			applied[ref.SiteID] = ref.Seq
		}
	}
	return applied
}

// NewDocumentFromSnapshot creates a full replica from a snapshot. An empty
// siteID gets a fresh random identity, which is the safe default: reusing a
// site ID is only sound when the snapshot covers every operation that site
// ever distributed.
func NewDocumentFromSnapshot(siteID string, snapshot Snapshot, opts ...DocumentOption) (*Document, error) {
	if snapshot.Version != ProtocolVersion {
		return nil, fmt.Errorf("%w: snapshot version %d", ErrUnsupportedVersion, snapshot.Version)
	}
	if snapshot.BaseHash == "" {
		return nil, fmt.Errorf("%w: snapshot missing base hash", ErrUnsupportedVersion)
	}

	doc := NewDocument(siteID, opts...)
	doc.baseHash = snapshot.BaseHash
	doc.clock = snapshot.Clock
	doc.compacted = cloneVectorClock(snapshot.VectorClock)
	doc.frontier.frontier = cloneVectorClock(snapshot.VectorClock)
	doc.localSeq = snapshot.VectorClock[doc.siteID]
	if applied := snapshot.Applied[doc.siteID]; applied > doc.localSeq {
		doc.localSeq = applied
	}
	if doc.siteID == snapshot.SiteID {
		doc.syncedThrough = snapshot.SyncedThrough
	}

	for _, fs := range snapshot.Features {
		state, err := restoreFeature(fs)
		if err != nil {
			return nil, err
		}
		doc.features[state.id] = state
	}

	pending := cloneDocumentOps(snapshot.PendingOps)
	pendingRefs := make(map[OpRef]struct{}, len(pending))
	for _, op := range pending {
		pendingRefs[op.ref()] = struct{}{}
	}

	// Sparse operations whose effects are already in Features re-enter only
	// the retained log and identity index. Reapplying them would duplicate
	// non-idempotent bookkeeping in the restored snapshot state.
	retained := cloneDocumentOps(snapshot.RetainedOps)
	sort.SliceStable(retained, func(i, j int) bool {
		if retained[i].SiteID != retained[j].SiteID {
			return retained[i].SiteID < retained[j].SiteID
		}
		return retained[i].Seq < retained[j].Seq
	})
	for _, op := range retained {
		normalized, err := normalizeSnapshotOp(op)
		if err != nil {
			return nil, fmt.Errorf("snapshot retained op: %w", err)
		}
		if _, isPending := pendingRefs[normalized.ref()]; isPending {
			continue
		}
		if known, dup := doc.seen[normalized.ref()]; dup {
			hash, hashErr := hashDocumentOp(normalized)
			if hashErr != nil {
				return nil, hashErr
			}
			if known != hash {
				return nil, fmt.Errorf("%w: %s", ErrIdentityCollision, normalized.ref())
			}
			continue
		}
		doc.recordOp(normalized)
	}

	// Outbox effects are also folded into Features. Restore their payloads
	// into the log so PendingOps can resume publication after a crash.
	for _, op := range cloneDocumentOps(snapshot.OutboxOps) {
		normalized, err := normalizeSnapshotOp(op)
		if err != nil {
			return nil, fmt.Errorf("snapshot outbox op: %w", err)
		}
		if known, dup := doc.seen[normalized.ref()]; dup {
			hash, hashErr := hashDocumentOp(normalized)
			if hashErr != nil {
				return nil, hashErr
			}
			if known != hash {
				return nil, fmt.Errorf("%w: %s", ErrIdentityCollision, normalized.ref())
			}
			continue
		}
		doc.recordOp(normalized)
	}

	// Pending operations re-enter the document application path to rebuild
	// dependency buffers that are not materialized in feature snapshots.
	sort.SliceStable(pending, func(i, j int) bool {
		return pending[i].stamp().less(pending[j].stamp())
	})
	for _, op := range pending {
		normalized, err := normalizeSnapshotOp(op)
		if err != nil {
			return nil, fmt.Errorf("snapshot pending op: %w", err)
		}
		if _, dup := doc.seen[normalized.ref()]; dup {
			continue
		}
		doc.applyOpLocked(normalized)
		doc.recordOp(normalized)
	}
	return doc, nil
}

func normalizeSnapshotOp(op DocumentOp) (DocumentOp, error) {
	op = op.normalize()
	if op.GeometryOp != nil {
		geometryOp, err := op.GeometryOp.deriveCreatedIDs()
		if err != nil {
			return DocumentOp{}, err
		}
		op.GeometryOp = &geometryOp
	}
	if err := op.validateEnvelope(); err != nil {
		return DocumentOp{}, err
	}
	return op, nil
}

func snapshotFeature(state *featureState) (FeatureSnapshot, bool) {
	fs := FeatureSnapshot{ID: state.id, Base: state.isBase}
	empty := !state.isBase

	if state.createReg.isSet() {
		reg := state.createReg
		fs.CreateReg = &reg
		empty = false
	}
	if state.deleteReg.isSet() {
		reg := state.deleteReg
		fs.DeleteReg = &reg
		empty = false
	}
	if state.genID.isSet() {
		genID := state.genID
		genStamp := state.genStamp
		fs.GenID = &genID
		fs.GenStamp = &genStamp
		empty = false
	}
	for ref := range state.seenGens {
		if ref.isSet() {
			fs.SeenGens = append(fs.SeenGens, ref)
			empty = false
		}
	}
	sort.Slice(fs.SeenGens, func(i, j int) bool {
		if fs.SeenGens[i].SiteID != fs.SeenGens[j].SiteID {
			return fs.SeenGens[i].SiteID < fs.SeenGens[j].SiteID
		}
		return fs.SeenGens[i].Seq < fs.SeenGens[j].Seq
	})
	if len(state.properties) > 0 {
		fs.Properties = make(map[string]PropertySnapshot, len(state.properties))
		for key, prop := range state.properties {
			ps := PropertySnapshot{
				Value:   cloneRawMessage(prop.value),
				Deleted: prop.deleted,
			}
			if prop.ref.isSet() {
				reg := prop.ref
				ps.Reg = &reg
			}
			fs.Properties[key] = ps
		}
		empty = false
	}
	if state.geometry != nil {
		fs.Geometry = snapshotGeometry(state.geometry)
		empty = false
	}
	return fs, empty
}

func restoreFeature(fs FeatureSnapshot) (*featureState, error) {
	state := &featureState{
		id:         fs.ID,
		isBase:     fs.Base,
		seenGens:   make(map[OpRef]struct{}),
		properties: make(map[string]propertyState, len(fs.Properties)),
	}
	if fs.Base {
		state.seenGens[OpRef{}] = struct{}{}
	}
	if fs.CreateReg != nil {
		state.createReg = *fs.CreateReg
	}
	if fs.DeleteReg != nil {
		state.deleteReg = *fs.DeleteReg
	}
	if fs.GenID != nil {
		state.genID = *fs.GenID
		state.seenGens[state.genID] = struct{}{}
		if fs.GenStamp != nil {
			state.genStamp = *fs.GenStamp
		}
	}
	for _, ref := range fs.SeenGens {
		state.seenGens[ref] = struct{}{}
	}
	for key, ps := range fs.Properties {
		prop := propertyState{
			value:   cloneRawMessage(ps.Value),
			deleted: ps.Deleted,
		}
		if ps.Reg != nil {
			prop.ref = *ps.Reg
		}
		state.properties[key] = prop
	}
	if fs.Geometry != nil {
		geometry, err := restoreGeometry(fs.Geometry)
		if err != nil {
			return nil, fmt.Errorf("feature %s: %w", fs.ID, err)
		}
		state.geometry = geometry
	}
	return state, nil
}

func snapshotGeometry(g *geometryState) *GeometrySnapshot {
	snapshot := &GeometrySnapshot{Type: g.typ, Dims: g.dims}
	for _, part := range g.parts {
		ps := PartSnapshot{
			ID:      part.id,
			Key:     snapshotKey(part.key),
			Type:    part.typ,
			Deleted: part.deleted,
		}
		for _, ring := range part.rings {
			rs := RingSnapshot{
				ID:       ring.id,
				Key:      snapshotKey(ring.key),
				Exterior: ring.exterior,
				Deleted:  ring.deleted,
			}
			ring.seq.walk(func(el *element) {
				vs := VertexSnapshot{
					ID:      el.id,
					Parent:  el.parentID,
					Key:     snapshotKey(el.key),
					Coord:   g.position(el.coord),
					Deleted: el.deleted,
				}
				if el.moveReg.isSet() {
					reg := el.moveReg
					vs.MoveReg = &reg
				}
				rs.Vertices = append(rs.Vertices, vs)
			})
			ps.Rings = append(ps.Rings, rs)
		}
		snapshot.Parts = append(snapshot.Parts, ps)
	}
	return snapshot
}

func restoreGeometry(snapshot *GeometrySnapshot) (*geometryState, error) {
	if snapshot.Dims != 2 && snapshot.Dims != 3 {
		return nil, fmt.Errorf("%w: snapshot dims %d", ErrInvalidGeometry, snapshot.Dims)
	}
	switch snapshot.Type {
	case GeometryPoint, GeometryLineString, GeometryPolygon,
		GeometryMultiPoint, GeometryMultiLine, GeometryMultiPolygon:
	default:
		return nil, fmt.Errorf("%w: snapshot geometry type %q", ErrUnsupportedGeometry, snapshot.Type)
	}

	g := &geometryState{
		typ:       snapshot.Type,
		dims:      snapshot.Dims,
		partsByID: make(map[string]*partState),
		pending:   make(map[depKey][]GeometryOp),
	}
	for _, ps := range snapshot.Parts {
		part := &partState{
			id:        ps.ID,
			key:       restoreKey(ps.Key),
			typ:       ps.Type,
			deleted:   ps.Deleted,
			ringsByID: make(map[string]*ringState),
		}
		for _, rs := range ps.Rings {
			ring := &ringState{
				id:       rs.ID,
				key:      restoreKey(rs.Key),
				exterior: rs.Exterior,
				deleted:  rs.Deleted,
				seq:      newVertexSeq(),
			}
			for _, vs := range rs.Vertices {
				if vs.Parent != "" && !ring.seq.has(vs.Parent) {
					return nil, fmt.Errorf("%w: snapshot vertex %q precedes its parent %q", ErrInvalidGeometry, vs.ID, vs.Parent)
				}
				coord, _, err := parsePosition(vs.Coord)
				if err != nil {
					return nil, err
				}
				if !ring.seq.insert(vs.ID, vs.Parent, restoreKey(vs.Key), coord) {
					return nil, fmt.Errorf("%w: duplicate snapshot vertex %q", ErrInvalidGeometry, vs.ID)
				}
				if vs.MoveReg != nil {
					ring.seq.byID[vs.ID].moveReg = *vs.MoveReg
				}
				if vs.Deleted {
					ring.seq.delete(vs.ID)
				}
			}
			part.rings = append(part.rings, ring)
			part.ringsByID[ring.id] = ring
		}
		g.parts = append(g.parts, part)
		g.partsByID[part.id] = part
	}
	return g, nil
}

func snapshotKey(key seqKey) KeySnapshot {
	if key.initial {
		pos := key.pos
		return KeySnapshot{Init: &pos}
	}
	stamp := key.stamp
	return KeySnapshot{Stamp: &stamp}
}

func restoreKey(key KeySnapshot) seqKey {
	if key.Init != nil {
		return initialKey(*key.Init)
	}
	if key.Stamp != nil {
		return opKey(*key.Stamp)
	}
	return seqKey{}
}
