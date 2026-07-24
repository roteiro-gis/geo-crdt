package crdt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func testFeatureCollection() json.RawMessage {
	return json.RawMessage(`{
		"type":"FeatureCollection",
		"features":[{
			"type":"Feature",
			"id":"parcel-1",
			"geometry":{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]},
			"properties":{"owner":"Smith","zone":"R1"}
		}]
	}`)
}

func newFCDocument(t testing.TB, siteID string) *Document {
	t.Helper()
	doc, err := NewDocumentFromFeatureCollection("test-document", siteID, testFeatureCollection())
	if err != nil {
		t.Fatalf("new document: %v", err)
	}
	return doc
}

func mustApply(t testing.TB, doc *Document, command any) {
	t.Helper()
	if err := doc.Apply(command); err != nil {
		t.Fatalf("apply %T: %v", command, err)
	}
}

func mustMergeDocs(t testing.TB, dst, src *Document) MergeResult {
	t.Helper()
	result, err := dst.Merge(src)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return result
}

func documentJSON(t testing.TB, doc *Document) string {
	t.Helper()
	data, err := doc.FeatureCollectionJSON()
	if err != nil {
		t.Fatalf("feature collection: %v", err)
	}
	return string(data)
}

func TestDocumentFeatureCollectionEdit(t *testing.T) {
	doc := newFCDocument(t, "site-a")

	mustApply(t, doc, SetProperty{FeatureID: "parcel-1", Key: "owner", Value: "Jones"})
	mustApply(t, doc, MoveFeatureVertex{
		FeatureID: "parcel-1",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 0),
		VertexID:  InitialVertexID(0, 1),
		Coord:     Coord{X: 8, Y: 0},
	})

	feature, ok := doc.Feature("parcel-1")
	if !ok {
		t.Fatal("expected parcel feature")
	}
	if feature.Properties["owner"] != "Jones" {
		t.Fatalf("owner = %v, want Jones", feature.Properties["owner"])
	}
	var geometry struct {
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
		t.Fatal(err)
	}
	if geometry.Coordinates[0][1][0] != 8 {
		t.Fatalf("moved x = %v, want 8", geometry.Coordinates[0][1][0])
	}
}

func TestDocumentMergeConvergesPropertiesAndGeometry(t *testing.T) {
	siteA := newFCDocument(t, "site-a")
	siteB := newFCDocument(t, "site-b")

	mustApply(t, siteA, SetProperty{FeatureID: "parcel-1", Key: "owner", Value: "Jones"})
	mustApply(t, siteB, MoveFeatureVertex{
		FeatureID: "parcel-1",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 0),
		VertexID:  InitialVertexID(0, 2),
		Coord:     Coord{X: 11, Y: 11},
	})

	mustMergeDocs(t, siteA, siteB)
	mustMergeDocs(t, siteB, siteA)

	if a, b := documentJSON(t, siteA), documentJSON(t, siteB); a != b {
		t.Fatalf("documents diverged:\nA=%s\nB=%s", a, b)
	}
}

func TestDocumentDeltaTransfersInsertedFeature(t *testing.T) {
	siteA := NewDocument("test-document", "site-a")
	siteB := NewDocument("test-document", "site-b")

	mustApply(t, siteA, InsertFeature{
		FeatureID:  "trail-1",
		Geometry:   json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`),
		Properties: map[string]any{"name": "North Trail"},
	})

	delta := siteA.DeltaSince(nil)
	if len(delta.Ops) != 1 {
		t.Fatalf("delta ops = %d, want 1", len(delta.Ops))
	}
	if _, err := siteB.MergeDelta(delta); err != nil {
		t.Fatalf("merge delta: %v", err)
	}

	feature, ok := siteB.Feature("trail-1")
	if !ok {
		t.Fatal("expected inserted feature on site B")
	}
	if feature.Properties["name"] != "North Trail" {
		t.Fatalf("name = %v, want North Trail", feature.Properties["name"])
	}
}

func TestDocumentMultipartGeometryPartEdit(t *testing.T) {
	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "roads",
		Geometry:  json.RawMessage(`{"type":"MultiLineString","coordinates":[[[0,0],[1,1]],[[10,10],[11,11]]]}`),
	})
	mustApply(t, doc, MoveFeatureVertex{
		FeatureID: "roads",
		PartID:    InitialPartID(1),
		RingID:    InitialRingID(1, 0),
		VertexID:  InitialVertexID(0, 1),
		Coord:     Coord{X: 12, Y: 12},
	})

	feature, _ := doc.Feature("roads")
	var geometry struct {
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
		t.Fatal(err)
	}
	if geometry.Coordinates[1][1][0] != 12 {
		t.Fatalf("unexpected second part coordinates: %+v", geometry.Coordinates[1])
	}
}

// Regression (review finding A2): a concurrent re-insert with a smaller
// geometry plus an edit against the old geometry must never brick sync.
func TestDocumentConcurrentReinsertAndEditConverge(t *testing.T) {
	siteA := NewDocument("test-document", "site-a")
	siteB := NewDocument("test-document", "site-b")

	mustApply(t, siteA, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[20,0],[20,20],[0,20],[0,0]],[[5,5],[5,8],[8,8],[8,5],[5,5]]]}`),
	})
	if _, err := siteB.MergeDelta(siteA.DeltaSince(nil)); err != nil {
		t.Fatal(err)
	}

	// Site A edits the hole (two more local ops raise its clock).
	mustApply(t, siteA, SetProperty{FeatureID: "f", Key: "note", Value: "editing"})
	mustApply(t, siteA, MoveFeatureVertex{
		FeatureID: "f",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 1),
		VertexID:  InitialVertexID(1, 0),
		Coord:     Coord{X: 6, Y: 6},
	})
	// Site B concurrently replaces the feature with a Point.
	mustApply(t, siteB, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[1,2]}`),
	})

	if _, err := siteA.MergeDelta(siteB.DeltaSince(siteA.VectorClock())); err != nil {
		t.Fatalf("merge B into A must not fail: %v", err)
	}
	if _, err := siteB.MergeDelta(siteA.DeltaSince(siteB.VectorClock())); err != nil {
		t.Fatalf("merge A into B must not fail: %v", err)
	}

	jsonA, jsonB := documentJSON(t, siteA), documentJSON(t, siteB)
	if jsonA != jsonB {
		t.Fatalf("documents diverged:\nA=%s\nB=%s", jsonA, jsonB)
	}
	// Sync stays healthy afterwards.
	mustApply(t, siteA, SetProperty{FeatureID: "f", Key: "after", Value: true})
	if _, err := siteB.MergeDelta(siteA.DeltaSince(siteB.VectorClock())); err != nil {
		t.Fatalf("sync after conflict must keep working: %v", err)
	}
}

// Regression (review finding A6): replicas with different bases refuse to
// merge instead of silently diverging.
func TestDocumentBaseMismatchRejected(t *testing.T) {
	withBase := newFCDocument(t, "site-a")
	empty := NewDocument("test-document", "site-b")
	mustApply(t, empty, InsertFeature{
		FeatureID: "x",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})

	if _, err := empty.Merge(withBase); !errors.Is(err, ErrBaseMismatch) {
		t.Fatalf("expected ErrBaseMismatch, got %v", err)
	}
	if _, err := withBase.Merge(empty); !errors.Is(err, ErrBaseMismatch) {
		t.Fatalf("expected ErrBaseMismatch, got %v", err)
	}
	if _, err := withBase.MergeDelta(empty.DeltaSince(nil)); !errors.Is(err, ErrBaseMismatch) {
		t.Fatalf("expected ErrBaseMismatch via delta, got %v", err)
	}

	// Identical collections loaded independently do merge.
	sameBase := newFCDocument(t, "site-c")
	if _, err := sameBase.Merge(withBase); err != nil {
		t.Fatalf("same base must merge: %v", err)
	}
}

// Regression (review finding A10): a poisoned timestamp cannot wedge the
// local clock.
func TestDocumentClockPoisoningRejected(t *testing.T) {
	doc := NewDocument("test-document", "site-a")
	_, err := doc.MergeOps("test-document", []DocumentOp{{
		Type:      OpDeleteFeature,
		SiteID:    "evil",
		Seq:       1,
		Timestamp: MaxTimestamp,
		FeatureID: "x",
	}})
	if !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("expected ErrInvalidOp, got %v", err)
	}
	mustApply(t, doc, InsertFeature{
		FeatureID: "ok",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
}

// Regression (review finding A11): insert_feature property payloads are
// validated as JSON at the envelope.
func TestDocumentInvalidPropertyJSONRejected(t *testing.T) {
	doc := NewDocument("test-document", "site-a")
	_, err := doc.MergeOps("test-document", []DocumentOp{{
		Type:       OpInsertFeature,
		SiteID:     "site-b",
		Seq:        1,
		Timestamp:  1,
		FeatureID:  "x",
		Geometry:   json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
		Properties: map[string]json.RawMessage{"bad": json.RawMessage(`{oops`)},
	}})
	if !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("expected ErrInvalidOp, got %v", err)
	}
}

// Regression (review finding A9): features without geometry are supported.
func TestDocumentNullGeometryFeature(t *testing.T) {
	fc := json.RawMessage(`{
		"type":"FeatureCollection",
		"features":[{"type":"Feature","id":"note-1","geometry":null,"properties":{"text":"hi"}}]
	}`)
	doc, err := NewDocumentFromFeatureCollection("test-document", "site-a", fc)
	if err != nil {
		t.Fatalf("null geometry FC: %v", err)
	}
	feature, ok := doc.Feature("note-1")
	if !ok {
		t.Fatal("expected feature")
	}
	if feature.Geometry != nil {
		t.Fatalf("geometry = %s, want nil", feature.Geometry)
	}
	mustApply(t, doc, SetProperty{FeatureID: "note-1", Key: "text", Value: "hello"})

	if err := doc.Apply(MoveFeatureVertex{FeatureID: "note-1", PartID: "part:0", RingID: "ring:0:0", VertexID: "init:0:0", Coord: Coord{}}); err == nil {
		t.Fatal("expected vertex edit on null geometry to fail")
	}

	exported := documentJSON(t, doc)
	if !strings.Contains(exported, `"geometry":null`) {
		t.Fatalf("export should carry null geometry: %s", exported)
	}

	// Round trip through InsertFeature with nil geometry.
	mustApply(t, doc, InsertFeature{FeatureID: "note-2", Properties: map[string]any{"k": 1}})
	if feature, ok := doc.Feature("note-2"); !ok || feature.Geometry != nil {
		t.Fatalf("nil-geometry insert failed: %+v ok=%v", feature, ok)
	}
}

func TestDocumentSetGeometryPreservesProperties(t *testing.T) {
	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID:  "f",
		Geometry:   json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
		Properties: map[string]any{"name": "spot"},
	})
	mustApply(t, doc, SetGeometry{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`),
	})

	feature, _ := doc.Feature("f")
	if feature.Properties["name"] != "spot" {
		t.Fatalf("properties lost on SetGeometry: %+v", feature.Properties)
	}
	var geom struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(feature.Geometry, &geom); err != nil || geom.Type != "LineString" {
		t.Fatalf("geometry not replaced: %s (%v)", feature.Geometry, err)
	}
}

func TestDocumentDeleteAndResurrect(t *testing.T) {
	siteA := NewDocument("test-document", "site-a")
	mustApply(t, siteA, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	mustApply(t, siteA, DeleteFeature{FeatureID: "f"})
	if _, ok := siteA.Feature("f"); ok {
		t.Fatal("feature should be deleted")
	}
	mustApply(t, siteA, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[5,5]}`),
	})
	feature, ok := siteA.Feature("f")
	if !ok {
		t.Fatal("newer insert must resurrect the feature")
	}
	var geom struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Geometry, &geom); err != nil || geom.Coordinates[0] != 5 {
		t.Fatalf("unexpected geometry after resurrect: %s", feature.Geometry)
	}
}

// Edits arriving before the insert that defines their geometry generation
// buffer and drain automatically.
func TestDocumentEditBeforeInsertBuffers(t *testing.T) {
	source := NewDocument("test-document", "site-a")
	mustApply(t, source, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`),
	})
	mustApply(t, source, MoveFeatureVertex{
		FeatureID: "f",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 0),
		VertexID:  InitialVertexID(0, 1),
		Coord:     Coord{X: 2, Y: 2},
	})
	ops := source.Ops()

	target := NewDocument("test-document", "site-b")
	result, err := target.MergeOps("test-document", ops[1:]) // edit first
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Buffered) != 1 {
		t.Fatalf("expected edit to buffer, got %+v", result)
	}
	if _, err := target.MergeOps("test-document", ops[:1]); err != nil {
		t.Fatal(err)
	}
	if a, b := documentJSON(t, source), documentJSON(t, target); a != b {
		t.Fatalf("documents diverged:\nA=%s\nB=%s", a, b)
	}
}

func TestDocumentAddRingAndPartCommands(t *testing.T) {
	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "parcel",
		Geometry:  json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[20,0],[20,20],[0,20],[0,0]]]}`),
	})
	mustApply(t, doc, AddFeatureRing{
		FeatureID: "parcel",
		PartID:    InitialPartID(0),
		Coords:    []Coord{{X: 5, Y: 5}, {X: 5, Y: 8}, {X: 8, Y: 8}, {X: 8, Y: 5}},
	})
	info, ok := doc.GeometryInfo("parcel")
	if !ok || len(info[0].Rings) != 2 {
		t.Fatalf("expected 2 rings, got %+v", info)
	}
	holeID := info[0].Rings[1].ID
	mustApply(t, doc, RemoveFeatureRing{FeatureID: "parcel", PartID: InitialPartID(0), RingID: holeID})
	info, _ = doc.GeometryInfo("parcel")
	if len(info[0].Rings) != 1 {
		t.Fatalf("expected hole removed, got %+v", info)
	}

	mustApply(t, doc, InsertFeature{
		FeatureID: "pipes",
		Geometry:  json.RawMessage(`{"type":"MultiLineString","coordinates":[[[0,0],[1,1]]]}`),
	})
	mustApply(t, doc, AddFeaturePart{
		FeatureID: "pipes",
		Geometry:  json.RawMessage(`{"type":"LineString","coordinates":[[5,5],[6,6]]}`),
	})
	info, _ = doc.GeometryInfo("pipes")
	if len(info) != 2 {
		t.Fatalf("expected 2 parts, got %+v", info)
	}
	mustApply(t, doc, RemoveFeaturePart{FeatureID: "pipes", PartID: InitialPartID(0)})
	info, _ = doc.GeometryInfo("pipes")
	if len(info) != 1 {
		t.Fatalf("expected 1 part after removal, got %+v", info)
	}
}

func TestDocumentPropertyLWWAndTombstones(t *testing.T) {
	siteA := newFCDocument(t, "site-a")
	siteB := newFCDocument(t, "site-b")

	mustApply(t, siteA, SetProperty{FeatureID: "parcel-1", Key: "owner", Value: "A"})
	mustApply(t, siteB, DeleteProperty{FeatureID: "parcel-1", Key: "owner"})

	mustMergeDocs(t, siteA, siteB)
	mustMergeDocs(t, siteB, siteA)

	featureA, _ := siteA.Feature("parcel-1")
	featureB, _ := siteB.Feature("parcel-1")
	if fmt.Sprint(featureA.Properties) != fmt.Sprint(featureB.Properties) {
		t.Fatalf("properties diverged: %v vs %v", featureA.Properties, featureB.Properties)
	}
	// Same timestamp: site-b's delete wins the tiebreak.
	if _, ok := featureA.Properties["owner"]; ok {
		t.Fatalf("expected owner deleted, got %v", featureA.Properties)
	}
}

// Regression: reusing a site identity beyond its local history is detected.
func TestDocumentSiteIdentityReuseDetected(t *testing.T) {
	original := NewDocument("test-document", "site-a")
	mustApply(t, original, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	peer := NewDocument("test-document", "site-b")
	if _, err := peer.Merge(original); err != nil {
		t.Fatal(err)
	}

	// A fresh document reuses "site-a" without its history.
	impostor := NewDocument("test-document", "site-a")
	if _, err := impostor.Merge(peer); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("expected identity reuse detection, got %v", err)
	}
}

func TestDocumentZCoordinateRoundTrip(t *testing.T) {
	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "peak",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[7.6,45.9,4810]}`),
	})
	feature, _ := doc.Feature("peak")
	var geom struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Geometry, &geom); err != nil {
		t.Fatal(err)
	}
	if len(geom.Coordinates) != 3 || geom.Coordinates[2] != 4810 {
		t.Fatalf("altitude lost: %v", geom.Coordinates)
	}
}

// TestDocumentConvergence_RandomPartialDeliveries mirrors the geometry-level
// randomized test at the document level.
func TestDocumentConvergence_RandomPartialDeliveries(t *testing.T) {
	for seed := int64(0); seed < 25; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			replicaCount := 2 + rng.Intn(3)
			var allOps []DocumentOp
			for i := 0; i < replicaCount; i++ {
				replica := NewDocument("test-document", fmt.Sprintf("site-%d", i))
				seedDocument(t, rng, replica)
				allOps = append(allOps, replica.Ops()...)
			}

			var converged string
			for i := 0; i < replicaCount; i++ {
				replica := NewDocument("test-document", fmt.Sprintf("merge-%d", i))
				shuffled := append([]DocumentOp(nil), allOps...)
				rng.Shuffle(len(shuffled), func(a, b int) {
					shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
				})
				for len(shuffled) > 0 {
					batch := 1 + rng.Intn(len(shuffled))
					if _, err := replica.MergeOps("test-document", shuffled[:batch]); err != nil {
						t.Fatalf("merge batch: %v", err)
					}
					shuffled = shuffled[batch:]
				}
				got := documentJSON(t, replica)
				if converged == "" {
					converged = got
				} else if got != converged {
					t.Fatalf("replicas diverged:\n%s\n%s", got, converged)
				}
			}
		})
	}
}

// seedDocument drives a random but locally valid edit history.
func seedDocument(t testing.TB, rng *rand.Rand, doc *Document) {
	t.Helper()
	featureIDs := []ID{"alpha", "beta", "gamma"}
	geometries := []string{
		`{"type":"Point","coordinates":[1,2]}`,
		`{"type":"LineString","coordinates":[[0,0],[5,5],[10,0]]}`,
		`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`,
	}

	for step := 0; step < 15; step++ {
		id := featureIDs[rng.Intn(len(featureIDs))]
		_, exists := doc.Feature(id)
		if !exists {
			if err := doc.Apply(InsertFeature{
				FeatureID:  id,
				Geometry:   json.RawMessage(geometries[rng.Intn(len(geometries))]),
				Properties: map[string]any{"step": step},
			}); err != nil {
				t.Fatalf("insert: %v", err)
			}
			continue
		}
		switch rng.Intn(5) {
		case 0:
			if err := doc.Apply(SetProperty{FeatureID: id, Key: fmt.Sprintf("k%d", rng.Intn(3)), Value: step}); err != nil {
				t.Fatalf("set property: %v", err)
			}
		case 1:
			if err := doc.Apply(DeleteFeature{FeatureID: id}); err != nil {
				t.Fatalf("delete feature: %v", err)
			}
		case 2:
			if err := doc.Apply(InsertFeature{
				FeatureID: id,
				Geometry:  json.RawMessage(geometries[rng.Intn(len(geometries))]),
			}); err != nil {
				t.Fatalf("re-insert: %v", err)
			}
		default:
			info, ok := doc.GeometryInfo(id)
			if !ok || len(info) == 0 || len(info[0].Rings) == 0 || len(info[0].Rings[0].Vertices) == 0 {
				continue
			}
			ring := info[0].Rings[0]
			vertex := ring.Vertices[rng.Intn(len(ring.Vertices))]
			if err := doc.Apply(MoveFeatureVertex{
				FeatureID: id,
				PartID:    info[0].ID,
				RingID:    ring.ID,
				VertexID:  vertex.ID,
				Coord:     Coord{X: float64(rng.Intn(20)), Y: float64(rng.Intn(20))},
			}); err != nil {
				t.Fatalf("move vertex: %v", err)
			}
		}
	}
}
