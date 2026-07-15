package crdt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

const (
	part0 = "part:0"
	ring0 = "ring:0:0"
)

// makePolygonJSON creates a GeoJSON Polygon from closed coordinate rings.
func makePolygonJSON(coords [][][2]float64) json.RawMessage {
	jsonCoords := make([][][]float64, len(coords))
	for i, ring := range coords {
		jsonCoords[i] = make([][]float64, len(ring))
		for j, pt := range ring {
			jsonCoords[i][j] = []float64{pt[0], pt[1]}
		}
	}
	data, err := json.Marshal(struct {
		Type        string        `json:"type"`
		Coordinates [][][]float64 `json:"coordinates"`
	}{Type: "Polygon", Coordinates: jsonCoords})
	if err != nil {
		panic(err)
	}
	return data
}

// squareCoords returns a closed counter-clockwise square polygon ring.
func squareCoords() [][][2]float64 {
	return [][][2]float64{
		{
			{0, 0},
			{10, 0},
			{10, 10},
			{0, 10},
			{0, 0}, // closure
		},
	}
}

// extractCoords parses closed polygon coordinates from GeoJSON.
func extractCoords(t *testing.T, data json.RawMessage) [][][2]float64 {
	t.Helper()
	var geom struct {
		Type        string        `json:"type"`
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(data, &geom); err != nil {
		t.Fatalf("unmarshal polygon: %v", err)
	}
	if geom.Type != "Polygon" {
		t.Fatalf("geometry type = %s, want Polygon", geom.Type)
	}
	coords := make([][][2]float64, len(geom.Coordinates))
	for i, ring := range geom.Coordinates {
		coords[i] = make([][2]float64, len(ring))
		for j, pt := range ring {
			coords[i][j] = [2]float64{pt[0], pt[1]}
		}
	}
	return coords
}

func newTestCRDT(t testing.TB, siteID string, initial json.RawMessage) *GeometryCRDT {
	t.Helper()
	c, err := NewGeometryCRDT(siteID, initial)
	if err != nil {
		t.Fatalf("new geometry CRDT: %v", err)
	}
	return c
}

// vertexAt resolves the stable ID of a visible exterior-ring vertex.
func vertexAt(t testing.TB, c *GeometryCRDT, index int) string {
	t.Helper()
	id, err := c.VertexIDAt(part0, ring0, index)
	if err != nil {
		t.Fatalf("vertex id at %d: %v", index, err)
	}
	return id
}

// --- Basic vertex operations ---

func TestInsertVertex(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	// Insert between visible vertices 1 and 2.
	if err := c.Apply(InsertVertexOp(part0, ring0, vertexAt(t, c, 1), Coord{X: 10, Y: 5})); err != nil {
		t.Fatalf("apply insert: %v", err)
	}

	coords := extractCoords(t, c.Geometry())
	if len(coords[0]) != 6 { // 4 open + 1 inserted + closure
		t.Fatalf("expected 6 positions after insert, got %d", len(coords[0]))
	}
	if coords[0][2] != [2]float64{10, 5} {
		t.Errorf("inserted vertex = %v, want [10 5]", coords[0][2])
	}
}

func TestInsertVertexAtHead(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	if err := c.Apply(InsertVertexOp(part0, ring0, "", Coord{X: -1, Y: -1})); err != nil {
		t.Fatalf("apply insert at head: %v", err)
	}
	coords := extractCoords(t, c.Geometry())
	if coords[0][0] != [2]float64{-1, -1} {
		t.Errorf("head vertex = %v, want [-1 -1]", coords[0][0])
	}
	if coords[0][len(coords[0])-1] != [2]float64{-1, -1} {
		t.Errorf("ring closure should follow the new head, got %v", coords[0][len(coords[0])-1])
	}
}

func TestDeleteVertex(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	if err := c.Apply(DeleteVertexOp(part0, ring0, vertexAt(t, c, 1))); err != nil {
		t.Fatalf("apply delete: %v", err)
	}

	coords := extractCoords(t, c.Geometry())
	if len(coords[0]) != 4 { // 3 open + closure
		t.Fatalf("expected 4 positions after delete, got %d", len(coords[0]))
	}
	if coords[0][1] != [2]float64{10, 10} {
		t.Errorf("vertex[1] = %v, want [10 10]", coords[0][1])
	}
}

func TestMoveVertex(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	if err := c.Apply(MoveVertexOp(part0, ring0, vertexAt(t, c, 2), Coord{X: 15, Y: 15})); err != nil {
		t.Fatalf("apply move: %v", err)
	}
	coords := extractCoords(t, c.Geometry())
	if coords[0][2] != [2]float64{15, 15} {
		t.Errorf("moved vertex = %v, want [15 15]", coords[0][2])
	}
}

// Regression (review finding A1): the closing coordinate is not an
// independent vertex, so editing vertex 0 keeps the ring closed.
func TestMoveFirstVertexKeepsRingClosed(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	if err := c.Apply(MoveVertexOp(part0, ring0, vertexAt(t, c, 0), Coord{X: -5, Y: -5})); err != nil {
		t.Fatalf("apply move: %v", err)
	}
	coords := extractCoords(t, c.Geometry())
	ring := coords[0]
	if ring[0] != [2]float64{-5, -5} {
		t.Fatalf("first vertex = %v, want [-5 -5]", ring[0])
	}
	if ring[0] != ring[len(ring)-1] {
		t.Fatalf("ring not closed after moving vertex 0: first=%v last=%v", ring[0], ring[len(ring)-1])
	}
}

func TestDeleteFirstVertexKeepsRingClosed(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	if err := c.Apply(DeleteVertexOp(part0, ring0, vertexAt(t, c, 0))); err != nil {
		t.Fatalf("apply delete: %v", err)
	}
	coords := extractCoords(t, c.Geometry())
	ring := coords[0]
	if len(ring) != 4 {
		t.Fatalf("expected 4 positions, got %d", len(ring))
	}
	if ring[0] != ring[len(ring)-1] {
		t.Fatalf("ring not closed after deleting vertex 0: %v", ring)
	}
}

// Regression (review finding A13): deletes cannot push geometry below its
// structural floor.
func TestDeleteVertexFloor(t *testing.T) {
	polygon := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,0],[5,10],[0,0]]]}`))
	if err := polygon.Apply(DeleteVertexOp(part0, ring0, vertexAt(t, polygon, 0))); err == nil {
		t.Fatal("expected polygon-ring floor to reject delete below 3 vertices")
	}

	line := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`))
	lineVertex, err := line.VertexIDAt(part0, ring0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := line.Apply(DeleteVertexOp(part0, ring0, lineVertex)); err == nil {
		t.Fatal("expected LineString floor to reject delete below 2 vertices")
	}
}

// --- Merging ---

func mustMerge(t testing.TB, dst, src *GeometryCRDT) MergeResult {
	t.Helper()
	result, err := dst.Merge(src)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return result
}

func TestMergeConcurrentInserts(t *testing.T) {
	initial := makePolygonJSON(squareCoords())
	siteA := newTestCRDT(t, "site-a", initial)
	siteB := newTestCRDT(t, "site-b", initial)

	if err := siteA.Apply(InsertVertexOp(part0, ring0, vertexAt(t, siteA, 0), Coord{X: 5, Y: 0})); err != nil {
		t.Fatalf("site A insert: %v", err)
	}
	if err := siteB.Apply(InsertVertexOp(part0, ring0, vertexAt(t, siteB, 2), Coord{X: 5, Y: 10})); err != nil {
		t.Fatalf("site B insert: %v", err)
	}

	mustMerge(t, siteA, siteB)
	mustMerge(t, siteB, siteA)

	coordsA := extractCoords(t, siteA.Geometry())
	coordsB := extractCoords(t, siteB.Geometry())
	if len(coordsA[0]) != 7 { // 6 open + closure
		t.Fatalf("expected 7 positions, got %d", len(coordsA[0]))
	}
	if fmt.Sprint(coordsA) != fmt.Sprint(coordsB) {
		t.Fatalf("replicas diverged:\nA=%v\nB=%v", coordsA, coordsB)
	}
}

func TestMergeConcurrentMovesConverge(t *testing.T) {
	initial := makePolygonJSON(squareCoords())
	siteA := newTestCRDT(t, "site-a", initial)
	siteB := newTestCRDT(t, "site-b", initial)

	if err := siteA.Apply(MoveVertexOp(part0, ring0, vertexAt(t, siteA, 1), Coord{X: 8, Y: 0})); err != nil {
		t.Fatal(err)
	}
	if err := siteB.Apply(MoveVertexOp(part0, ring0, vertexAt(t, siteB, 1), Coord{X: 12, Y: 0})); err != nil {
		t.Fatal(err)
	}

	mustMerge(t, siteA, siteB)
	mustMerge(t, siteB, siteA)

	coordsA := extractCoords(t, siteA.Geometry())
	coordsB := extractCoords(t, siteB.Geometry())
	if coordsA[0][1] != coordsB[0][1] {
		t.Fatalf("concurrent moves diverged: A=%v B=%v", coordsA[0][1], coordsB[0][1])
	}
	// Same timestamp; "site-b" > "site-a" wins the tiebreak.
	if coordsA[0][1] != [2]float64{12, 0} {
		t.Fatalf("expected site-b move to win, got %v", coordsA[0][1])
	}
}

func TestConflictResolution_LWW_HigherTimestamp(t *testing.T) {
	initial := makePolygonJSON(squareCoords())
	siteA := newTestCRDT(t, "site-a", initial)
	siteB := newTestCRDT(t, "site-b", initial)

	// Site A moves twice (final timestamp 2); site B once (timestamp 1).
	if err := siteA.Apply(MoveVertexOp(part0, ring0, vertexAt(t, siteA, 1), Coord{X: 8, Y: 0})); err != nil {
		t.Fatal(err)
	}
	if err := siteA.Apply(MoveVertexOp(part0, ring0, vertexAt(t, siteA, 1), Coord{X: 9, Y: 0})); err != nil {
		t.Fatal(err)
	}
	if err := siteB.Apply(MoveVertexOp(part0, ring0, vertexAt(t, siteB, 1), Coord{X: 12, Y: 0})); err != nil {
		t.Fatal(err)
	}

	result := mustMerge(t, siteA, siteB)
	if len(result.Superseded) != 1 {
		t.Fatalf("expected site B's stale move to be reported superseded, got %+v", result)
	}
	coords := extractCoords(t, siteA.Geometry())
	if coords[0][1] != [2]float64{9, 0} {
		t.Fatalf("expected site A's second move to win, got %v", coords[0][1])
	}
}

func TestMerge_Idempotent(t *testing.T) {
	initial := makePolygonJSON(squareCoords())
	siteA := newTestCRDT(t, "site-a", initial)
	siteB := newTestCRDT(t, "site-b", initial)

	if err := siteA.Apply(InsertVertexOp(part0, ring0, vertexAt(t, siteA, 0), Coord{X: 5, Y: 0})); err != nil {
		t.Fatal(err)
	}
	mustMerge(t, siteB, siteA)
	result := mustMerge(t, siteB, siteA)
	if result.Applied != 0 || result.Duplicates != 1 {
		t.Fatalf("second merge should be pure duplicates, got %+v", result)
	}

	coords := extractCoords(t, siteB.Geometry())
	if len(coords[0]) != 6 {
		t.Fatalf("expected 6 positions after idempotent merge, got %d", len(coords[0]))
	}
}

func TestMerge_DifferentBaseGeometryRejected(t *testing.T) {
	polygon := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))
	line := newTestCRDT(t, "site-b", json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`))

	if _, err := polygon.Merge(line); !errors.Is(err, ErrBaseMismatch) {
		t.Fatalf("expected ErrBaseMismatch, got %v", err)
	}
}

func TestMergeOps_BuffersOutOfOrderDelivery(t *testing.T) {
	initial := makePolygonJSON(squareCoords())
	source := newTestCRDT(t, "site-b", initial)
	if err := source.Apply(InsertVertexOp(part0, ring0, vertexAt(t, source, 0), Coord{X: 5, Y: 0})); err != nil {
		t.Fatal(err)
	}
	parentID := source.Ops()[0].vertexID()
	if err := source.Apply(InsertVertexOp(part0, ring0, parentID, Coord{X: 7, Y: 0})); err != nil {
		t.Fatal(err)
	}
	ops := source.Ops()

	target := newTestCRDT(t, "site-a", initial)
	// Deliver the child before its parent, in separate batches.
	result, err := target.MergeOps(ops[1:])
	if err != nil {
		t.Fatalf("merge child first: %v", err)
	}
	if len(result.Buffered) != 1 {
		t.Fatalf("expected child op to buffer, got %+v", result)
	}
	result, err = target.MergeOps(ops[:1])
	if err != nil {
		t.Fatalf("merge parent: %v", err)
	}
	if result.Applied != 2 {
		t.Fatalf("expected parent + drained child to apply, got %+v", result)
	}
	if string(target.Geometry()) != string(source.Geometry()) {
		t.Fatalf("replicas diverged:\n%s\n%s", target.Geometry(), source.Geometry())
	}
}

// Regression (review finding A2): inapplicable remote operations quarantine
// instead of failing the merge.
func TestMergeOps_QuarantinesImpossibleOps(t *testing.T) {
	point := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"Point","coordinates":[1,2]}`))

	result, err := point.MergeOps([]GeometryOp{{
		Action:    ActionDeleteVertex,
		SiteID:    "site-b",
		Seq:       1,
		Timestamp: 1,
		PartID:    part0,
		RingID:    ring0,
		VertexID:  InitialVertexID(0, 0),
	}})
	if err != nil {
		t.Fatalf("merge must not fail on content: %v", err)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("expected quarantine, got %+v", result)
	}
	// The point remains editable.
	if err := point.Apply(MoveVertexOp(part0, ring0, InitialVertexID(0, 0), Coord{X: 3, Y: 4})); err != nil {
		t.Fatalf("apply after quarantine: %v", err)
	}
}

func TestMergeOps_InvalidEnvelopesRejected(t *testing.T) {
	tests := []struct {
		name string
		op   GeometryOp
	}{
		{"missing site", GeometryOp{Action: ActionMoveVertex, Seq: 1, Timestamp: 1, PartID: part0, RingID: ring0, VertexID: "x", Coord: []float64{1, 1}}},
		{"zero seq", GeometryOp{Action: ActionMoveVertex, SiteID: "b", Timestamp: 1, PartID: part0, RingID: ring0, VertexID: "x", Coord: []float64{1, 1}}},
		{"zero timestamp", GeometryOp{Action: ActionMoveVertex, SiteID: "b", Seq: 1, PartID: part0, RingID: ring0, VertexID: "x", Coord: []float64{1, 1}}},
		{"poisoned timestamp", GeometryOp{Action: ActionMoveVertex, SiteID: "b", Seq: 1, Timestamp: MaxTimestamp, PartID: part0, RingID: ring0, VertexID: "x", Coord: []float64{1, 1}}},
		{"unknown action", GeometryOp{Action: "scale_vertex", SiteID: "b", Seq: 1, Timestamp: 1, PartID: part0, RingID: ring0, VertexID: "x"}},
		{"missing target", GeometryOp{Action: ActionDeleteVertex, SiteID: "b", Seq: 1, Timestamp: 1, PartID: part0, RingID: ring0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))
			before := string(c.Geometry())
			if _, err := c.MergeOps([]GeometryOp{tt.op}); !errors.Is(err, ErrInvalidOp) {
				t.Fatalf("expected ErrInvalidOp, got %v", err)
			}
			if len(c.Ops()) != 0 {
				t.Fatal("invalid envelope mutated the op log")
			}
			if got := string(c.Geometry()); got != before {
				t.Fatalf("invalid envelope mutated geometry: %s", got)
			}
		})
	}
}

// Non-finite coordinates are content, not envelope: they quarantine so one
// broken op cannot block a batch, and they never reach the JSON encoder.
func TestMergeOps_NonFiniteCoordQuarantined(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))
	result, err := c.MergeOps([]GeometryOp{{
		Action:    ActionMoveVertex,
		SiteID:    "site-b",
		Seq:       1,
		Timestamp: 1,
		PartID:    part0,
		RingID:    ring0,
		VertexID:  InitialVertexID(0, 1),
		Coord:     []float64{math.NaN(), 0},
	}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("expected quarantine, got %+v", result)
	}
	if !json.Valid(c.Geometry()) {
		t.Fatal("geometry must remain valid JSON")
	}
}

func TestMergeOps_DeduplicatesIncomingBatch(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))
	op := GeometryOp{
		Action:        ActionInsertVertex,
		SiteID:        "site-b",
		Seq:           1,
		Timestamp:     1,
		PartID:        part0,
		RingID:        ring0,
		AfterVertexID: InitialVertexID(0, 0),
		Coord:         []float64{5, 0},
	}
	result, err := c.MergeOps([]GeometryOp{op, op})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 || result.Duplicates != 1 {
		t.Fatalf("expected 1 applied + 1 duplicate, got %+v", result)
	}
	coords := extractCoords(t, c.Geometry())
	if len(coords[0]) != 6 {
		t.Fatalf("expected one insert to apply, got %v", coords[0])
	}
}

// --- Ring and part operations ---

func TestAddAndRemoveRing(t *testing.T) {
	initial := makePolygonJSON([][][2]float64{
		{{0, 0}, {20, 0}, {20, 20}, {0, 20}, {0, 0}},
	})
	c := newTestCRDT(t, "site-a", initial)

	hole := []Coord{{X: 5, Y: 5}, {X: 5, Y: 15}, {X: 15, Y: 15}, {X: 15, Y: 5}}
	if err := c.Apply(AddRingOp(part0, hole)); err != nil {
		t.Fatalf("add ring: %v", err)
	}
	coords := extractCoords(t, c.Geometry())
	if len(coords) != 2 {
		t.Fatalf("expected 2 rings, got %d", len(coords))
	}
	if len(coords[1]) != 5 { // 4 open + closure
		t.Fatalf("expected closed 5-position hole, got %v", coords[1])
	}

	ringID := c.Ops()[0].ringID()
	if err := c.Apply(RemoveRingOp(part0, ringID)); err != nil {
		t.Fatalf("remove ring: %v", err)
	}
	coords = extractCoords(t, c.Geometry())
	if len(coords) != 1 {
		t.Fatalf("expected hole removed, got %d rings", len(coords))
	}

	// The exterior ring can never be removed.
	if err := c.Apply(RemoveRingOp(part0, ring0)); err == nil {
		t.Fatal("expected removing the exterior ring to fail locally")
	}
	result, err := c.MergeOps([]GeometryOp{{
		Action:    ActionRemoveRing,
		SiteID:    "site-b",
		Seq:       1,
		Timestamp: 9,
		PartID:    part0,
		RingID:    ring0,
	}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("expected remote exterior removal to quarantine, got %+v", result)
	}
}

func TestAddRingConvergesAcrossReplicas(t *testing.T) {
	initial := makePolygonJSON([][][2]float64{
		{{0, 0}, {20, 0}, {20, 20}, {0, 20}, {0, 0}},
	})
	siteA := newTestCRDT(t, "site-a", initial)
	siteB := newTestCRDT(t, "site-b", initial)

	if err := siteA.Apply(AddRingOp(part0, []Coord{{X: 5, Y: 5}, {X: 5, Y: 8}, {X: 8, Y: 8}, {X: 8, Y: 5}})); err != nil {
		t.Fatal(err)
	}
	if err := siteB.Apply(AddRingOp(part0, []Coord{{X: 12, Y: 12}, {X: 12, Y: 15}, {X: 15, Y: 15}, {X: 15, Y: 12}})); err != nil {
		t.Fatal(err)
	}
	mustMerge(t, siteA, siteB)
	mustMerge(t, siteB, siteA)

	if string(siteA.Geometry()) != string(siteB.Geometry()) {
		t.Fatalf("replicas diverged:\n%s\n%s", siteA.Geometry(), siteB.Geometry())
	}
	if coords := extractCoords(t, siteA.Geometry()); len(coords) != 3 {
		t.Fatalf("expected exterior + 2 holes, got %d rings", len(coords))
	}
}

func TestAddAndRemovePart(t *testing.T) {
	c := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"MultiLineString","coordinates":[[[0,0],[1,1]]]}`))

	if err := c.Apply(AddPartOp(json.RawMessage(`{"type":"LineString","coordinates":[[10,10],[11,11]]}`))); err != nil {
		t.Fatalf("add part: %v", err)
	}
	var geom struct {
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(c.Geometry(), &geom); err != nil {
		t.Fatal(err)
	}
	if len(geom.Coordinates) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(geom.Coordinates))
	}

	// Part type must match the multi type.
	if err := c.Apply(AddPartOp(json.RawMessage(`{"type":"Point","coordinates":[1,2]}`))); err == nil {
		t.Fatal("expected mismatched part type to fail locally")
	}

	if err := c.Apply(RemovePartOp(part0)); err != nil {
		t.Fatalf("remove part: %v", err)
	}
	if err := json.Unmarshal(c.Geometry(), &geom); err != nil {
		t.Fatal(err)
	}
	if len(geom.Coordinates) != 1 || geom.Coordinates[0][0][0] != 10 {
		t.Fatalf("expected only the added part to remain, got %v", geom.Coordinates)
	}

	// Part operations on simple geometries fail locally and quarantine
	// remotely.
	simple := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`))
	if err := simple.Apply(AddPartOp(json.RawMessage(`{"type":"LineString","coordinates":[[2,2],[3,3]]}`))); err == nil {
		t.Fatal("expected add_part on simple geometry to fail locally")
	}
	result, err := simple.MergeOps([]GeometryOp{{
		Action: ActionAddPart, SiteID: "site-b", Seq: 1, Timestamp: 1,
		Part: json.RawMessage(`{"type":"LineString","coordinates":[[2,2],[3,3]]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("expected quarantine, got %+v", result)
	}
}

// --- Geometry types, Z, and parsing ---

func TestPointOnlySupportsMove(t *testing.T) {
	c := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"Point","coordinates":[1,2]}`))

	if err := c.Apply(MoveVertexOp(part0, ring0, InitialVertexID(0, 0), Coord{X: 3, Y: 4})); err != nil {
		t.Fatalf("move point: %v", err)
	}
	var geom struct {
		Type        string    `json:"type"`
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(c.Geometry(), &geom); err != nil {
		t.Fatal(err)
	}
	if geom.Type != "Point" || geom.Coordinates[0] != 3 || geom.Coordinates[1] != 4 {
		t.Fatalf("unexpected point geometry: %+v", geom)
	}

	if err := c.Apply(InsertVertexOp(part0, ring0, "", Coord{X: 9, Y: 9})); err == nil {
		t.Fatal("expected insert on point to fail")
	}
}

// Regression (review finding A9): altitude is preserved end to end.
func TestZCoordinatesPreserved(t *testing.T) {
	c := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"LineString","coordinates":[[0,0,100],[1,1,200]]}`))

	if err := c.Apply(InsertVertexOp(part0, ring0, InitialVertexID(0, 0), Coord{X: 0.5, Y: 0.5, Z: 150})); err != nil {
		t.Fatal(err)
	}
	var geom struct {
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(c.Geometry(), &geom); err != nil {
		t.Fatal(err)
	}
	want := [][]float64{{0, 0, 100}, {0.5, 0.5, 150}, {1, 1, 200}}
	if fmt.Sprint(geom.Coordinates) != fmt.Sprint(want) {
		t.Fatalf("coordinates = %v, want %v", geom.Coordinates, want)
	}
}

func TestNewGeometryCRDT_InvalidInputs(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{"nil geometry", nil},
		{"malformed json", json.RawMessage(`{"type":"Point"`)},
		{"geometry collection", json.RawMessage(`{"type":"GeometryCollection","geometries":[]}`)},
		{"unknown type", json.RawMessage(`{"type":"Blob","coordinates":[]}`)},
		{"short coordinate", json.RawMessage(`{"type":"Point","coordinates":[1]}`)},
		{"one-position line", json.RawMessage(`{"type":"LineString","coordinates":[[0,0]]}`)},
		{"two-position ring", json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[1,1],[0,0]]]}`)},
		{"non-finite coordinate", json.RawMessage(`{"type":"Point","coordinates":[1e999,2]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewGeometryCRDT("site-a", tt.input); err == nil {
				t.Fatal("expected constructor error")
			}
		})
	}
}

func TestMultipartGeometriesSupported(t *testing.T) {
	c := newTestCRDT(t, "site-a", json.RawMessage(`{"type":"MultiPoint","coordinates":[[0,0],[1,1]]}`))
	if err := c.Apply(MoveVertexOp("part:1", "ring:1:0", InitialVertexID(0, 0), Coord{X: 5, Y: 5})); err != nil {
		t.Fatalf("move multipoint part: %v", err)
	}
	var geom struct {
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(c.Geometry(), &geom); err != nil {
		t.Fatal(err)
	}
	if geom.Coordinates[1][0] != 5 {
		t.Fatalf("unexpected multipoint coordinates: %v", geom.Coordinates)
	}
}

func TestGeometryOpJSONRoundTrip(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))
	if err := c.Apply(InsertVertexOp(part0, ring0, vertexAt(t, c, 0), Coord{X: 5, Y: 0})); err != nil {
		t.Fatal(err)
	}
	op := c.Ops()[0]

	data, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"applied", "synced", "position"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("local bookkeeping %q leaked onto the wire: %s", forbidden, data)
		}
	}

	var decoded GeometryOp
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%+v", decoded) != fmt.Sprintf("%+v", op) {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", decoded, op)
	}
}

// --- Sync watermarks ---

// Regression (review finding A7): operations applied between PendingOps and
// MarkSynced are not lost.
func TestPendingOpsWatermark(t *testing.T) {
	c := newTestCRDT(t, "site-a", makePolygonJSON(squareCoords()))

	if err := c.Apply(MoveVertexOp(part0, ring0, vertexAt(t, c, 1), Coord{X: 8, Y: 0})); err != nil {
		t.Fatal(err)
	}
	pending, watermark := c.PendingOps()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending op, got %d", len(pending))
	}

	// A concurrent local edit lands between PendingOps and MarkSynced.
	if err := c.Apply(MoveVertexOp(part0, ring0, vertexAt(t, c, 2), Coord{X: 11, Y: 11})); err != nil {
		t.Fatal(err)
	}
	c.MarkSynced(watermark)

	pending, _ = c.PendingOps()
	if len(pending) != 1 {
		t.Fatalf("op applied after PendingOps must stay pending, got %d", len(pending))
	}
	if pending[0].Seq != 2 {
		t.Fatalf("unexpected pending op: %+v", pending[0])
	}
}

// --- Convergence ---

func TestConvergence_ThreeSites(t *testing.T) {
	initial := makePolygonJSON(squareCoords())
	sites := []*GeometryCRDT{
		newTestCRDT(t, "site-a", initial),
		newTestCRDT(t, "site-b", initial),
		newTestCRDT(t, "site-c", initial),
	}
	if err := sites[0].Apply(MoveVertexOp(part0, ring0, vertexAt(t, sites[0], 0), Coord{X: -1, Y: -1})); err != nil {
		t.Fatal(err)
	}
	if err := sites[1].Apply(InsertVertexOp(part0, ring0, vertexAt(t, sites[1], 1), Coord{X: 10, Y: 5})); err != nil {
		t.Fatal(err)
	}
	if err := sites[2].Apply(MoveVertexOp(part0, ring0, vertexAt(t, sites[2], 3), Coord{X: 0, Y: 12})); err != nil {
		t.Fatal(err)
	}

	for _, dst := range sites {
		for _, src := range sites {
			mustMerge(t, dst, src)
		}
	}

	first := string(sites[0].Geometry())
	for i, site := range sites {
		if got := string(site.Geometry()); got != first {
			t.Fatalf("site %d diverged:\n%s\n%s", i, got, first)
		}
	}
}

func TestConvergence_StableVertexAddressingEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		editA func(*GeometryCRDT) error
		editB func(*GeometryCRDT) error
		check func(*testing.T, [][][2]float64)
	}{
		{
			name: "concurrent inserts after the same vertex keep both, newest first",
			editA: func(c *GeometryCRDT) error {
				return c.Apply(InsertVertexOp(part0, ring0, InitialVertexID(0, 1), Coord{X: 4, Y: 0}))
			},
			editB: func(c *GeometryCRDT) error {
				return c.Apply(InsertVertexOp(part0, ring0, InitialVertexID(0, 1), Coord{X: 6, Y: 0}))
			},
			check: func(t *testing.T, coords [][][2]float64) {
				t.Helper()
				if len(coords[0]) != 7 {
					t.Fatalf("expected 7 positions, got %d", len(coords[0]))
				}
				// Equal timestamps: site-b sorts closer to the shared parent.
				if coords[0][2] != [2]float64{6, 0} || coords[0][3] != [2]float64{4, 0} {
					t.Fatalf("unexpected insert order: %v", coords[0])
				}
			},
		},
		{
			name: "insert after a concurrently deleted vertex survives",
			editA: func(c *GeometryCRDT) error {
				return c.Apply(InsertVertexOp(part0, ring0, InitialVertexID(0, 0), Coord{X: 5, Y: 0}))
			},
			editB: func(c *GeometryCRDT) error {
				return c.Apply(DeleteVertexOp(part0, ring0, InitialVertexID(0, 0)))
			},
			check: func(t *testing.T, coords [][][2]float64) {
				t.Helper()
				found := false
				for _, coord := range coords[0] {
					if coord == [2]float64{5, 0} {
						found = true
					}
				}
				if !found {
					t.Fatalf("insert anchored to tombstone was lost: %v", coords[0])
				}
			},
		},
		{
			name: "delete suppresses concurrent move of the same vertex",
			editA: func(c *GeometryCRDT) error {
				return c.Apply(DeleteVertexOp(part0, ring0, InitialVertexID(0, 1)))
			},
			editB: func(c *GeometryCRDT) error {
				return c.Apply(MoveVertexOp(part0, ring0, InitialVertexID(0, 1), Coord{X: 99, Y: 99}))
			},
			check: func(t *testing.T, coords [][][2]float64) {
				t.Helper()
				for _, coord := range coords[0] {
					if coord == [2]float64{99, 99} {
						t.Fatalf("move of deleted vertex is visible: %v", coords[0])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initial := makePolygonJSON(squareCoords())
			siteA := newTestCRDT(t, "site-a", initial)
			siteB := newTestCRDT(t, "site-b", initial)

			if err := tt.editA(siteA); err != nil {
				t.Fatalf("site A edit: %v", err)
			}
			if err := tt.editB(siteB); err != nil {
				t.Fatalf("site B edit: %v", err)
			}
			mustMerge(t, siteA, siteB)
			mustMerge(t, siteB, siteA)

			coordsA := extractCoords(t, siteA.Geometry())
			coordsB := extractCoords(t, siteB.Geometry())
			if fmt.Sprint(coordsA) != fmt.Sprint(coordsB) {
				t.Fatalf("replicas diverged: A=%v B=%v", coordsA, coordsB)
			}
			tt.check(t, coordsA)
		})
	}
}

// TestConvergence_RandomPartialDeliveries drives random edit histories and
// delivers the combined op set to fresh replicas in shuffled batches of
// random sizes. Every replica must converge byte-identically and no merge
// may return an error.
func TestConvergence_RandomPartialDeliveries(t *testing.T) {
	initial := makePolygonJSON(squareCoords())

	for seed := int64(0); seed < 40; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			replicaCount := 2 + rng.Intn(3)
			var allOps []GeometryOp
			for i := 0; i < replicaCount; i++ {
				replica := newTestCRDT(t, fmt.Sprintf("site-%d", i), initial)
				for step := 0; step < 12; step++ {
					if err := replica.Apply(randomLocalOp(t, rng, replica)); err != nil {
						t.Fatalf("random local op: %v", err)
					}
				}
				allOps = append(allOps, replica.Ops()...)
			}

			var converged string
			for i := 0; i < replicaCount; i++ {
				replica := newTestCRDT(t, fmt.Sprintf("merge-%d", i), initial)
				shuffled := append([]GeometryOp(nil), allOps...)
				rng.Shuffle(len(shuffled), func(a, b int) {
					shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
				})
				for len(shuffled) > 0 {
					batch := 1 + rng.Intn(len(shuffled))
					if _, err := replica.MergeOps(shuffled[:batch]); err != nil {
						t.Fatalf("merge batch: %v", err)
					}
					shuffled = shuffled[batch:]
				}
				got := string(replica.Geometry())
				if converged == "" {
					converged = got
				} else if got != converged {
					t.Fatalf("replicas diverged:\n%s\n%s", got, converged)
				}
			}
		})
	}
}

func randomLocalOp(t testing.TB, rng *rand.Rand, c *GeometryCRDT) GeometryOp {
	t.Helper()
	info := c.Info()
	ring := info[0].Rings[0]
	count := len(ring.Vertices)

	coord := Coord{X: float64(rng.Intn(21) - 10), Y: float64(rng.Intn(21) - 10)}
	switch rng.Intn(3) {
	case 0:
		after := ""
		if pick := rng.Intn(count + 1); pick > 0 {
			after = ring.Vertices[pick-1].ID
		}
		return InsertVertexOp(part0, ring0, after, coord)
	case 1:
		return MoveVertexOp(part0, ring0, ring.Vertices[rng.Intn(count)].ID, coord)
	default:
		if count <= 3 {
			return MoveVertexOp(part0, ring0, ring.Vertices[rng.Intn(count)].ID, coord)
		}
		return DeleteVertexOp(part0, ring0, ring.Vertices[rng.Intn(count)].ID)
	}
}
