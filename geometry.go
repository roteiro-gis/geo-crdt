package crdt

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// geometryState is the convergent state of one GeoJSON geometry: an ordered
// set of parts, each holding an ordered set of rings, each holding a vertex
// sequence. Every sub-structure is commutative (ordered inserts, monotone
// tombstones, last-writer-wins registers), so operations apply incrementally
// in any delivery order. Operations whose dependencies have not arrived yet
// are buffered and drained when the dependency appears.
type geometryState struct {
	typ  GeometryType
	dims int // 2 or 3

	parts     []*partState
	partsByID map[string]*partState

	// pending buffers operations waiting on a missing part, ring, or vertex.
	pending map[depKey][]GeometryOp

	// cache holds the marshaled GeoJSON; nil means dirty.
	cache json.RawMessage
}

type partState struct {
	id      string
	key     seqKey
	typ     GeometryType // simple part type
	deleted bool

	rings     []*ringState
	ringsByID map[string]*ringState
}

type ringState struct {
	id       string
	key      seqKey
	exterior bool // polygon ring 0; cannot be removed
	deleted  bool
	seq      *vertexSeq
}

// setKeyBefore orders parts and rings within their collections: initial
// entries first in base order, then added entries oldest-first. (This differs
// from sibling vertex order, where insert-after semantics require newest
// first.)
func setKeyBefore(a, b seqKey) bool {
	if a.initial != b.initial {
		return a.initial
	}
	if a.initial {
		return a.pos < b.pos
	}
	if a.stamp.Timestamp != b.stamp.Timestamp {
		return a.stamp.Timestamp < b.stamp.Timestamp
	}
	return a.stamp.SiteID < b.stamp.SiteID
}

type depKind int

const (
	depPart depKind = iota
	depRing
	depVertex
)

// depKey identifies a dependency an operation may wait on. Vertex IDs are
// only unique within a ring, so ring and part scope is part of the key.
type depKey struct {
	kind   depKind
	partID string
	ringID string
	id     string
}

// applyOutcome classifies what happened to one operation.
type applyOutcome int

const (
	// outcomeApplied: the operation changed state.
	outcomeApplied applyOutcome = iota
	// outcomeDuplicate: the operation had already taken effect (idempotence).
	outcomeDuplicate
	// outcomeSuperseded: a newer conflicting write already won (LWW loss).
	outcomeSuperseded
	// outcomeBuffered: a dependency is missing; the operation waits for it.
	outcomeBuffered
	// outcomeQuarantined: the operation can never apply to this geometry
	// (wrong geometry type, removing an exterior ring, ...). Quarantine
	// decisions depend only on convergent state, so replicas agree.
	outcomeQuarantined
)

type applyResult struct {
	outcome applyOutcome
	dep     depKey // set when buffered
	reason  string // set when quarantined
}

func applied() applyResult    { return applyResult{outcome: outcomeApplied} }
func duplicate() applyResult  { return applyResult{outcome: outcomeDuplicate} }
func superseded() applyResult { return applyResult{outcome: outcomeSuperseded} }
func buffered(dep depKey) applyResult {
	return applyResult{outcome: outcomeBuffered, dep: dep}
}
func quarantined(format string, args ...any) applyResult {
	return applyResult{outcome: outcomeQuarantined, reason: fmt.Sprintf(format, args...)}
}

// opOutcome pairs a drained operation with what happened to it.
type opOutcome struct {
	ref     OpRef
	outcome applyOutcome
	reason  string
}

// --- Construction from GeoJSON ---

type geoJSONGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// newGeometryState parses a GeoJSON geometry into convergent state with
// deterministic initial part, ring, and vertex IDs.
func newGeometryState(geometry json.RawMessage) (*geometryState, error) {
	var header geoJSONGeometry
	if err := json.Unmarshal(geometry, &header); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidGeometry, err)
	}

	typ := GeometryType(header.Type)
	g := &geometryState{
		typ:       typ,
		dims:      2,
		partsByID: make(map[string]*partState),
		pending:   make(map[depKey][]GeometryOp),
	}

	var partCoords [][][][3]float64 // part -> ring -> vertex
	switch typ {
	case GeometryPoint:
		point, dims, err := parsePositionJSON(header.Coordinates)
		if err != nil {
			return nil, err
		}
		g.setDims(dims)
		partCoords = [][][][3]float64{{{point}}}
	case GeometryLineString:
		line, dims, err := parseLineJSON(header.Coordinates)
		if err != nil {
			return nil, err
		}
		g.setDims(dims)
		partCoords = [][][][3]float64{{line}}
	case GeometryPolygon:
		rings, dims, err := parsePolygonJSON(header.Coordinates)
		if err != nil {
			return nil, err
		}
		g.setDims(dims)
		partCoords = [][][][3]float64{rings}
	case GeometryMultiPoint:
		points, dims, err := parseLineJSON(header.Coordinates)
		if err != nil {
			return nil, err
		}
		g.setDims(dims)
		for _, point := range points {
			partCoords = append(partCoords, [][][3]float64{{point}})
		}
	case GeometryMultiLine:
		var raw []json.RawMessage
		if err := json.Unmarshal(header.Coordinates, &raw); err != nil {
			return nil, fmt.Errorf("%w: multilinestring coordinates: %v", ErrInvalidGeometry, err)
		}
		for _, lineJSON := range raw {
			line, dims, err := parseLineJSON(lineJSON)
			if err != nil {
				return nil, err
			}
			g.setDims(dims)
			partCoords = append(partCoords, [][][3]float64{line})
		}
	case GeometryMultiPolygon:
		var raw []json.RawMessage
		if err := json.Unmarshal(header.Coordinates, &raw); err != nil {
			return nil, fmt.Errorf("%w: multipolygon coordinates: %v", ErrInvalidGeometry, err)
		}
		for _, polygonJSON := range raw {
			rings, dims, err := parsePolygonJSON(polygonJSON)
			if err != nil {
				return nil, err
			}
			g.setDims(dims)
			partCoords = append(partCoords, rings)
		}
	case "GeometryCollection":
		return nil, fmt.Errorf("%w: GeometryCollection", ErrUnsupportedGeometry)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedGeometry, header.Type)
	}

	if typ == GeometryLineString && len(partCoords[0][0]) < 2 {
		return nil, fmt.Errorf("%w: LineString requires at least 2 positions", ErrInvalidGeometry)
	}

	partType := typ.partType()
	for i, ringCoords := range partCoords {
		if partType == GeometryLineString && len(ringCoords[0]) < 2 {
			return nil, fmt.Errorf("%w: LineString part %d requires at least 2 positions", ErrInvalidGeometry, i)
		}
		part := &partState{
			id:        InitialPartID(i),
			key:       initialKey(i),
			typ:       partType,
			ringsByID: make(map[string]*ringState),
		}
		for r, coords := range ringCoords {
			ring := &ringState{
				id:       InitialRingID(i, r),
				key:      initialKey(r),
				exterior: partType == GeometryPolygon && r == 0,
				seq:      newInitialSeq(r, coords),
			}
			part.rings = append(part.rings, ring)
			part.ringsByID[ring.id] = ring
		}
		g.parts = append(g.parts, part)
		g.partsByID[part.id] = part
	}
	return g, nil
}

func (g *geometryState) setDims(dims int) {
	if dims > g.dims {
		g.dims = dims
	}
}

// parsePositionJSON parses one GeoJSON position. Positions require two
// finite values; a third (altitude) is preserved, further values are ignored
// per RFC 7946's advice against them.
func parsePositionJSON(data json.RawMessage) ([3]float64, int, error) {
	var raw []float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return [3]float64{}, 0, fmt.Errorf("%w: position: %v", ErrInvalidGeometry, err)
	}
	return parsePosition(raw)
}

func parsePosition(raw []float64) ([3]float64, int, error) {
	if len(raw) < 2 {
		return [3]float64{}, 0, fmt.Errorf("%w: position requires at least 2 values", ErrInvalidGeometry)
	}
	dims := 2
	pos := [3]float64{raw[0], raw[1]}
	if len(raw) >= 3 {
		pos[2] = raw[2]
		dims = 3
	}
	for i := 0; i < dims; i++ {
		if !isFinite(pos[i]) {
			return [3]float64{}, 0, fmt.Errorf("%w: position values must be finite", ErrInvalidGeometry)
		}
	}
	return pos, dims, nil
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func parseLineJSON(data json.RawMessage) ([][3]float64, int, error) {
	var raw [][]float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, fmt.Errorf("%w: coordinates: %v", ErrInvalidGeometry, err)
	}
	return parseLine(raw)
}

func parseLine(raw [][]float64) ([][3]float64, int, error) {
	dims := 2
	line := make([][3]float64, len(raw))
	for i, position := range raw {
		pos, posDims, err := parsePosition(position)
		if err != nil {
			return nil, 0, err
		}
		if posDims > dims {
			dims = posDims
		}
		line[i] = pos
	}
	return line, dims, nil
}

// parsePolygonJSON parses polygon rings into open coordinate sequences:
// a closing coordinate equal to the first is stripped, so ring closure is a
// property of the export, not of CRDT state. Each ring requires at least
// three distinct positions.
func parsePolygonJSON(data json.RawMessage) ([][][3]float64, int, error) {
	var raw [][][]float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, fmt.Errorf("%w: polygon coordinates: %v", ErrInvalidGeometry, err)
	}
	if len(raw) == 0 {
		return nil, 0, fmt.Errorf("%w: polygon requires at least one ring", ErrInvalidGeometry)
	}
	dims := 2
	rings := make([][][3]float64, len(raw))
	for i, rawRing := range raw {
		ring, ringDims, err := parseLine(rawRing)
		if err != nil {
			return nil, 0, err
		}
		if ringDims > dims {
			dims = ringDims
		}
		ring = openRing(ring)
		if len(ring) < 3 {
			return nil, 0, fmt.Errorf("%w: polygon ring %d requires at least 3 distinct positions", ErrInvalidGeometry, i)
		}
		rings[i] = ring
	}
	return rings, dims, nil
}

// openRing strips the closing coordinate when the ring is closed.
func openRing(ring [][3]float64) [][3]float64 {
	if len(ring) >= 2 && ring[0] == ring[len(ring)-1] {
		return ring[:len(ring)-1]
	}
	return ring
}

// --- Operation application ---

// apply routes one identified operation into the state. It returns the
// primary operation's result plus outcomes for any previously buffered
// operations drained by it. Buffered operations are stored inside the state
// and retried automatically when their dependency arrives.
func (g *geometryState) apply(op GeometryOp) (applyResult, []opOutcome) {
	result := g.applyOnce(op)
	switch result.outcome {
	case outcomeBuffered:
		g.pending[result.dep] = append(g.pending[result.dep], op)
		return result, nil
	case outcomeApplied:
		g.cache = nil
		return result, g.drain(g.unlockedDeps(op))
	default:
		return result, nil
	}
}

// unlockedDeps lists the dependencies satisfied by a successfully applied
// operation.
func (g *geometryState) unlockedDeps(op GeometryOp) []depKey {
	switch op.Action {
	case ActionInsertVertex:
		return []depKey{{depVertex, op.PartID, op.RingID, op.vertexID()}}
	case ActionAddRing:
		ringID := op.ringID()
		deps := []depKey{{depRing, op.PartID, "", ringID}}
		for i := range op.Ring {
			deps = append(deps, depKey{depVertex, op.PartID, ringID, addRingVertexID(op.ref(), i)})
		}
		return deps
	case ActionAddPart:
		partID := op.partID()
		part := g.partsByID[partID]
		deps := []depKey{{depPart, "", "", partID}}
		for _, ring := range part.rings {
			deps = append(deps, depKey{depRing, partID, "", ring.id})
			ring.seq.walk(func(el *element) {
				deps = append(deps, depKey{depVertex, partID, ring.id, el.id})
			})
		}
		return deps
	}
	return nil
}

// drain retries operations buffered on newly satisfied dependencies. Applying
// a drained operation can satisfy further dependencies, so this loops until
// quiescent. Ops are commutative, so drain order does not affect final state.
func (g *geometryState) drain(deps []depKey) []opOutcome {
	var outcomes []opOutcome
	for len(deps) > 0 {
		dep := deps[0]
		deps = deps[1:]
		waiting := g.pending[dep]
		if len(waiting) == 0 {
			continue
		}
		delete(g.pending, dep)
		for _, op := range waiting {
			result := g.applyOnce(op)
			switch result.outcome {
			case outcomeBuffered:
				g.pending[result.dep] = append(g.pending[result.dep], op)
			case outcomeApplied:
				g.cache = nil
				deps = append(deps, g.unlockedDeps(op)...)
				outcomes = append(outcomes, opOutcome{ref: op.ref(), outcome: outcomeApplied})
			default:
				outcomes = append(outcomes, opOutcome{ref: op.ref(), outcome: result.outcome, reason: result.reason})
			}
		}
	}
	return outcomes
}

func (g *geometryState) applyOnce(op GeometryOp) applyResult {
	switch op.Action {
	case ActionInsertVertex, ActionMoveVertex, ActionDeleteVertex:
		return g.applyVertexAction(op)
	case ActionAddRing:
		return g.applyAddRing(op)
	case ActionRemoveRing:
		return g.applyRemoveRing(op)
	case ActionAddPart:
		return g.applyAddPart(op)
	case ActionRemovePart:
		return g.applyRemovePart(op)
	default:
		// Unknown actions are rejected by envelope validation; reaching this
		// indicates a caller bug.
		return quarantined("unknown geometry action %q", op.Action)
	}
}

// resolvePart finds the target part or classifies why it cannot be found.
// On simple geometries the part set is static, so an unknown part ID can
// never be satisfied; on multipart geometries it may arrive via add_part.
func (g *geometryState) resolvePart(op GeometryOp) (*partState, applyResult) {
	part, ok := g.partsByID[op.PartID]
	if ok {
		return part, applyResult{}
	}
	if !g.typ.isMulti() {
		return nil, quarantined("part %q does not exist on simple geometry %s", op.PartID, g.typ)
	}
	return nil, buffered(depKey{depPart, "", "", op.PartID})
}

// resolveRing finds the target ring within a part. Point and LineString
// parts have a static single ring, so unknown ring IDs quarantine; polygon
// rings may arrive via add_ring.
func (g *geometryState) resolveRing(part *partState, op GeometryOp) (*ringState, applyResult) {
	ring, ok := part.ringsByID[op.RingID]
	if ok {
		return ring, applyResult{}
	}
	if part.typ != GeometryPolygon {
		return nil, quarantined("ring %q does not exist on %s part %q", op.RingID, part.typ, part.id)
	}
	return nil, buffered(depKey{depRing, part.id, "", op.RingID})
}

func (g *geometryState) applyVertexAction(op GeometryOp) applyResult {
	part, fail := g.resolvePart(op)
	if part == nil {
		return fail
	}
	ring, fail := g.resolveRing(part, op)
	if ring == nil {
		return fail
	}
	if part.typ == GeometryPoint && op.Action != ActionMoveVertex {
		return quarantined("Point parts support move_vertex only")
	}

	switch op.Action {
	case ActionInsertVertex:
		vertexID := op.vertexID()
		if ring.seq.has(vertexID) {
			return duplicate()
		}
		if fail := checkWireCoord(op.Coord); fail.reason != "" {
			return fail
		}
		if op.AfterVertexID != "" && !ring.seq.has(op.AfterVertexID) {
			return buffered(depKey{depVertex, part.id, ring.id, op.AfterVertexID})
		}
		ring.seq.insert(vertexID, op.AfterVertexID, opKey(op.stamp()), coordFromOp(op.Coord))
		return applied()
	case ActionMoveVertex:
		if fail := checkWireCoord(op.Coord); fail.reason != "" {
			return fail
		}
		if !ring.seq.has(op.VertexID) {
			return buffered(depKey{depVertex, part.id, ring.id, op.VertexID})
		}
		if !ring.seq.move(op.VertexID, op.stamp(), coordFromOp(op.Coord)) {
			return superseded()
		}
		return applied()
	default: // ActionDeleteVertex
		if !ring.seq.has(op.VertexID) {
			return buffered(depKey{depVertex, part.id, ring.id, op.VertexID})
		}
		if !ring.seq.delete(op.VertexID) {
			return duplicate()
		}
		return applied()
	}
}

func (g *geometryState) applyAddRing(op GeometryOp) applyResult {
	part, fail := g.resolvePart(op)
	if part == nil {
		return fail
	}
	if part.typ != GeometryPolygon {
		return quarantined("add_ring targets %s part %q; only Polygon parts hold rings", part.typ, part.id)
	}
	ringID := op.ringID()
	if _, exists := part.ringsByID[ringID]; exists {
		return duplicate()
	}
	coords, _, err := parseLine(op.Ring)
	if err != nil {
		return quarantined("add_ring coordinates: %v", err)
	}
	coords = openRing(coords)
	if len(coords) < 3 {
		return quarantined("add_ring requires at least 3 distinct positions")
	}

	ring := &ringState{id: ringID, key: opKey(op.stamp()), seq: newVertexSeq()}
	parentID := ""
	for i, coord := range coords {
		id := addRingVertexID(op.ref(), i)
		ring.seq.mustInsert(id, parentID, initialKey(i), coord)
		parentID = id
	}
	insertRing(part, ring)
	return applied()
}

func (g *geometryState) applyRemoveRing(op GeometryOp) applyResult {
	part, fail := g.resolvePart(op)
	if part == nil {
		return fail
	}
	ring, fail := g.resolveRing(part, op)
	if ring == nil {
		return fail
	}
	if ring.exterior {
		return quarantined("exterior ring %q cannot be removed", ring.id)
	}
	if ring.deleted {
		return duplicate()
	}
	ring.deleted = true
	return applied()
}

func (g *geometryState) applyAddPart(op GeometryOp) applyResult {
	if !g.typ.isMulti() {
		return quarantined("add_part targets simple geometry %s", g.typ)
	}
	partID := op.partID()
	if _, exists := g.partsByID[partID]; exists {
		return duplicate()
	}

	partGeom, err := newGeometryState(op.Part)
	if err != nil {
		return quarantined("add_part geometry: %v", err)
	}
	if partGeom.typ != g.typ.partType() {
		return quarantined("add_part geometry is %s; %s requires %s parts", partGeom.typ, g.typ, g.typ.partType())
	}

	// Re-identify the parsed part under IDs derived from this operation.
	source := partGeom.parts[0]
	part := &partState{
		id:        partID,
		key:       opKey(op.stamp()),
		typ:       source.typ,
		ringsByID: make(map[string]*ringState),
	}
	for r, sourceRing := range source.rings {
		ring := &ringState{
			id:       addPartRingID(op.ref(), r),
			key:      initialKey(r),
			exterior: sourceRing.exterior,
			seq:      newVertexSeq(),
		}
		parentID := ""
		for i, coord := range sourceRing.seq.visibleCoords() {
			id := addPartVertexID(op.ref(), r, i)
			ring.seq.mustInsert(id, parentID, initialKey(i), coord)
			parentID = id
		}
		part.rings = append(part.rings, ring)
		part.ringsByID[ring.id] = ring
	}

	at := sort.Search(len(g.parts), func(i int) bool {
		return setKeyBefore(part.key, g.parts[i].key)
	})
	g.parts = append(g.parts, nil)
	copy(g.parts[at+1:], g.parts[at:])
	g.parts[at] = part
	g.partsByID[part.id] = part
	return applied()
}

func (g *geometryState) applyRemovePart(op GeometryOp) applyResult {
	if !g.typ.isMulti() {
		return quarantined("remove_part targets simple geometry %s", g.typ)
	}
	part, fail := g.resolvePart(op)
	if part == nil {
		return fail
	}
	if part.deleted {
		return duplicate()
	}
	part.deleted = true
	return applied()
}

func insertRing(part *partState, ring *ringState) {
	at := sort.Search(len(part.rings), func(i int) bool {
		return setKeyBefore(ring.key, part.rings[i].key)
	})
	part.rings = append(part.rings, nil)
	copy(part.rings[at+1:], part.rings[at:])
	part.rings[at] = ring
	part.ringsByID[ring.id] = ring
}

func coordFromOp(raw []float64) [3]float64 {
	var coord [3]float64
	copy(coord[:], raw)
	return coord
}

// checkWireCoord quarantines operations whose coordinate payload could never
// be valid; non-finite values would make the geometry unencodable as JSON.
func checkWireCoord(coord []float64) applyResult {
	if len(coord) < 2 || len(coord) > 3 {
		return quarantined("coordinate requires 2 or 3 values, got %d", len(coord))
	}
	for _, v := range coord {
		if !isFinite(v) {
			return quarantined("coordinate values must be finite")
		}
	}
	return applyResult{}
}

// Derived stable IDs for operation-created structures. Deriving IDs from the
// operation identity keeps the wire format small and makes IDs impossible to
// mismatch.
func addRingVertexID(ref OpRef, i int) string {
	return fmt.Sprintf("v:%s:%d:%d", ref.SiteID, ref.Seq, i)
}

func addPartRingID(ref OpRef, r int) string {
	return fmt.Sprintf("ring:%s:%d:%d", ref.SiteID, ref.Seq, r)
}

func addPartVertexID(ref OpRef, r, i int) string {
	return fmt.Sprintf("v:%s:%d:%d:%d", ref.SiteID, ref.Seq, r, i)
}

// --- Local validation ---

// validateLocalOp strictly validates an operation about to be created
// locally: all referenced structure must exist now, geometry-type rules and
// vertex-count floors are enforced, and coordinates must match the geometry.
// Remote operations never pass through here; merges buffer or quarantine
// instead.
func (g *geometryState) validateLocalOp(op GeometryOp) error {
	switch op.Action {
	case ActionInsertVertex, ActionMoveVertex, ActionDeleteVertex:
		part, ok := g.partsByID[op.PartID]
		if !ok {
			return fmt.Errorf("%w: part %q does not exist", ErrInvalidCommand, op.PartID)
		}
		ring, ok := part.ringsByID[op.RingID]
		if !ok {
			return fmt.Errorf("%w: ring %q does not exist in part %q", ErrInvalidCommand, op.RingID, op.PartID)
		}
		if part.deleted || ring.deleted {
			return fmt.Errorf("%w: target ring %q is deleted", ErrInvalidCommand, op.RingID)
		}
		switch op.Action {
		case ActionInsertVertex:
			if part.typ == GeometryPoint {
				return fmt.Errorf("%w: Point geometry supports move_vertex only", ErrInvalidCommand)
			}
			if err := validateOpCoord(op.Coord); err != nil {
				return err
			}
			if op.AfterVertexID != "" && !ring.seq.has(op.AfterVertexID) {
				return fmt.Errorf("%w: after_vertex_id %q does not exist", ErrInvalidCommand, op.AfterVertexID)
			}
		case ActionMoveVertex:
			if err := validateOpCoord(op.Coord); err != nil {
				return err
			}
			if !ring.seq.has(op.VertexID) {
				return fmt.Errorf("%w: vertex %q does not exist", ErrInvalidCommand, op.VertexID)
			}
		case ActionDeleteVertex:
			if part.typ == GeometryPoint {
				return fmt.Errorf("%w: Point geometry supports move_vertex only", ErrInvalidCommand)
			}
			if !ring.seq.has(op.VertexID) {
				return fmt.Errorf("%w: vertex %q does not exist", ErrInvalidCommand, op.VertexID)
			}
			floor := 2 // LineString
			if part.typ == GeometryPolygon {
				floor = 3
			}
			if ring.seq.visible-1 < floor {
				return fmt.Errorf("%w: delete would leave %s ring below %d vertices", ErrInvalidCommand, part.typ, floor)
			}
		}
	case ActionAddRing:
		part, ok := g.partsByID[op.PartID]
		if !ok {
			return fmt.Errorf("%w: part %q does not exist", ErrInvalidCommand, op.PartID)
		}
		if part.typ != GeometryPolygon {
			return fmt.Errorf("%w: add_ring requires a Polygon part", ErrInvalidCommand)
		}
		coords, _, err := parseLine(op.Ring)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCommand, err)
		}
		if len(openRing(coords)) < 3 {
			return fmt.Errorf("%w: add_ring requires at least 3 distinct positions", ErrInvalidCommand)
		}
	case ActionRemoveRing:
		part, ok := g.partsByID[op.PartID]
		if !ok {
			return fmt.Errorf("%w: part %q does not exist", ErrInvalidCommand, op.PartID)
		}
		ring, ok := part.ringsByID[op.RingID]
		if !ok {
			return fmt.Errorf("%w: ring %q does not exist", ErrInvalidCommand, op.RingID)
		}
		if ring.exterior {
			return fmt.Errorf("%w: exterior ring cannot be removed", ErrInvalidCommand)
		}
		if ring.deleted {
			return fmt.Errorf("%w: ring %q is already deleted", ErrInvalidCommand, op.RingID)
		}
	case ActionAddPart:
		if !g.typ.isMulti() {
			return fmt.Errorf("%w: add_part requires a multipart geometry", ErrInvalidCommand)
		}
		partGeom, err := newGeometryState(op.Part)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCommand, err)
		}
		if partGeom.typ != g.typ.partType() {
			return fmt.Errorf("%w: %s requires %s parts, got %s", ErrInvalidCommand, g.typ, g.typ.partType(), partGeom.typ)
		}
	case ActionRemovePart:
		if !g.typ.isMulti() {
			return fmt.Errorf("%w: remove_part requires a multipart geometry", ErrInvalidCommand)
		}
		part, ok := g.partsByID[op.PartID]
		if !ok {
			return fmt.Errorf("%w: part %q does not exist", ErrInvalidCommand, op.PartID)
		}
		if part.deleted {
			return fmt.Errorf("%w: part %q is already deleted", ErrInvalidCommand, op.PartID)
		}
	default:
		return fmt.Errorf("%w: unknown geometry action %q", ErrInvalidCommand, op.Action)
	}
	return nil
}

func validateOpCoord(coord []float64) error {
	if len(coord) < 2 || len(coord) > 3 {
		return fmt.Errorf("%w: coordinate requires 2 or 3 values", ErrInvalidCommand)
	}
	for _, v := range coord {
		if !isFinite(v) {
			return fmt.Errorf("%w: coordinate values must be finite", ErrInvalidCommand)
		}
	}
	return nil
}

// --- Reads ---

// VertexInfo describes one visible vertex.
type VertexInfo struct {
	ID    string
	Coord Coord
}

// RingInfo describes one visible ring or coordinate sequence.
type RingInfo struct {
	ID       string
	Exterior bool
	Vertices []VertexInfo
}

// PartInfo describes one visible geometry part.
type PartInfo struct {
	ID    string
	Type  GeometryType
	Rings []RingInfo
}

// info returns the visible structure with stable IDs, for editors that need
// to address vertices.
func (g *geometryState) info() []PartInfo {
	parts := make([]PartInfo, 0, len(g.parts))
	for _, part := range g.parts {
		if part.deleted {
			continue
		}
		info := PartInfo{ID: part.id, Type: part.typ}
		for _, ring := range part.rings {
			if ring.deleted {
				continue
			}
			info.Rings = append(info.Rings, RingInfo{
				ID:       ring.id,
				Exterior: ring.exterior,
				Vertices: ring.seq.visibleVertices(),
			})
		}
		parts = append(parts, info)
	}
	return parts
}

// vertexIDAt resolves a visible vertex index within a ring to its stable ID.
func (g *geometryState) vertexIDAt(partID, ringID string, index int) (string, error) {
	part, ok := g.partsByID[partID]
	if !ok {
		return "", fmt.Errorf("%w: part %q does not exist", ErrInvalidCommand, partID)
	}
	ring, ok := part.ringsByID[ringID]
	if !ok {
		return "", fmt.Errorf("%w: ring %q does not exist", ErrInvalidCommand, ringID)
	}
	return ring.seq.vertexIDAt(index)
}

// pendingOps returns buffered operations in deterministic order, for
// snapshot serialization.
func (g *geometryState) pendingOps() []GeometryOp {
	var ops []GeometryOp
	for _, waiting := range g.pending {
		ops = append(ops, waiting...)
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Timestamp != ops[j].Timestamp {
			return ops[i].Timestamp < ops[j].Timestamp
		}
		return ops[i].SiteID < ops[j].SiteID
	})
	return ops
}

// --- GeoJSON export ---

// geoJSON returns the current geometry as GeoJSON, using a cached encoding
// when the state has not changed.
func (g *geometryState) geoJSON() json.RawMessage {
	if g.cache == nil {
		g.cache = g.marshalGeoJSON()
	}
	result := make(json.RawMessage, len(g.cache))
	copy(result, g.cache)
	return result
}

func (g *geometryState) marshalGeoJSON() json.RawMessage {
	var coordinates any
	switch g.typ {
	case GeometryPoint:
		coordinates = g.marshalPoint(g.parts[0])
	case GeometryLineString:
		coordinates = g.marshalLine(g.parts[0])
	case GeometryPolygon:
		coordinates = g.marshalPolygon(g.parts[0])
	case GeometryMultiPoint:
		points := make([][]float64, 0, len(g.parts))
		for _, part := range g.parts {
			if !part.deleted {
				points = append(points, g.marshalPoint(part))
			}
		}
		coordinates = points
	case GeometryMultiLine:
		lines := make([][][]float64, 0, len(g.parts))
		for _, part := range g.parts {
			if !part.deleted {
				lines = append(lines, g.marshalLine(part))
			}
		}
		coordinates = lines
	case GeometryMultiPolygon:
		polygons := make([][][][]float64, 0, len(g.parts))
		for _, part := range g.parts {
			if !part.deleted {
				polygons = append(polygons, g.marshalPolygon(part))
			}
		}
		coordinates = polygons
	}

	data, err := json.Marshal(struct {
		Type        string `json:"type"`
		Coordinates any    `json:"coordinates"`
	}{Type: string(g.typ), Coordinates: coordinates})
	if err != nil {
		// Coordinates are plain float slices; marshaling cannot fail.
		panic(fmt.Sprintf("crdt: marshal geometry: %v", err))
	}
	return data
}

func (g *geometryState) marshalPoint(part *partState) []float64 {
	coords := part.rings[0].seq.visibleCoords()
	return g.position(coords[0])
}

func (g *geometryState) marshalLine(part *partState) [][]float64 {
	coords := part.rings[0].seq.visibleCoords()
	line := make([][]float64, len(coords))
	for i, coord := range coords {
		line[i] = g.position(coord)
	}
	return line
}

func (g *geometryState) marshalPolygon(part *partState) [][][]float64 {
	rings := make([][][]float64, 0, len(part.rings))
	for _, ring := range part.rings {
		if ring.deleted {
			continue
		}
		coords := ring.seq.visibleCoords()
		encoded := make([][]float64, 0, len(coords)+1)
		for _, coord := range coords {
			encoded = append(encoded, g.position(coord))
		}
		// Close the ring on export; CRDT state stores open rings.
		if len(encoded) > 0 {
			encoded = append(encoded, encoded[0])
		}
		rings = append(rings, encoded)
	}
	return rings
}

func (g *geometryState) position(coord [3]float64) []float64 {
	if g.dims >= 3 {
		return []float64{coord[0], coord[1], coord[2]}
	}
	return []float64{coord[0], coord[1]}
}
