package crdt

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDocumentMergeOwnsNestedGeometryPayloads(t *testing.T) {
	t.Parallel()

	base := json.RawMessage(`{
		"type":"FeatureCollection",
		"features":[{
			"type":"Feature",
			"id":"f",
			"geometry":{"type":"LineString","coordinates":[[0,0],[1,1]]},
			"properties":{}
		}]
	}`)
	doc, err := NewDocumentFromFeatureCollection("test-document", "local", base)
	if err != nil {
		t.Fatal(err)
	}
	move := DocumentOp{
		Type: OpEditGeometry, SiteID: "actor", Seq: 2, Timestamp: 2, FeatureID: "f",
		GeometryOp: &GeometryOp{
			Action: ActionMoveVertex, PartID: InitialPartID(0), RingID: InitialRingID(0, 0),
			VertexID: InsertedVertexID("actor", 1), Coord: []float64{5, 5},
		},
	}
	if result, err := doc.MergeOps("test-document", []DocumentOp{move}); err != nil {
		t.Fatal(err)
	} else if len(result.Buffered) != 1 {
		t.Fatalf("move was not buffered: %+v", result)
	}
	move.GeometryOp.Coord[0] = 99

	insert := DocumentOp{
		Type: OpEditGeometry, SiteID: "actor", Seq: 1, Timestamp: 1, FeatureID: "f",
		GeometryOp: &GeometryOp{
			Action: ActionInsertVertex, PartID: InitialPartID(0), RingID: InitialRingID(0, 0),
			AfterVertexID: InitialVertexID(0, 0), Coord: []float64{2, 2},
		},
	}
	if _, err := doc.MergeOps("test-document", []DocumentOp{insert}); err != nil {
		t.Fatal(err)
	}
	feature, _ := doc.Feature("f")
	var geometry struct {
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
		t.Fatal(err)
	}
	if geometry.Coordinates[1][0] != 5 {
		t.Fatalf("caller mutation changed buffered coordinate: %v", geometry.Coordinates)
	}
}

func TestStandaloneMergeOwnsNestedGeometryPayloads(t *testing.T) {
	t.Parallel()

	replica, err := NewGeometryCRDT("local", json.RawMessage(
		`{"type":"LineString","coordinates":[[0,0],[1,1]]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	move := GeometryOp{
		Action: ActionMoveVertex, SiteID: "actor", Seq: 2, Timestamp: 2,
		PartID: InitialPartID(0), RingID: InitialRingID(0, 0),
		VertexID: InsertedVertexID("actor", 1), Coord: []float64{5, 5},
	}
	if result, err := replica.MergeOps([]GeometryOp{move}); err != nil {
		t.Fatal(err)
	} else if len(result.Buffered) != 1 {
		t.Fatalf("move was not buffered: %+v", result)
	}
	move.Coord[0] = 99

	insert := GeometryOp{
		Action: ActionInsertVertex, SiteID: "actor", Seq: 1, Timestamp: 1,
		PartID: InitialPartID(0), RingID: InitialRingID(0, 0),
		AfterVertexID: InitialVertexID(0, 0), Coord: []float64{2, 2},
	}
	if _, err := replica.MergeOps([]GeometryOp{insert}); err != nil {
		t.Fatal(err)
	}
	var geometry struct {
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(replica.Geometry(), &geometry); err != nil {
		t.Fatal(err)
	}
	if geometry.Coordinates[1][0] != 5 {
		t.Fatalf("caller mutation changed buffered coordinate: %v", geometry.Coordinates)
	}
}

func TestMemStoreCopiesSnapshotsOnSaveAndLoad(t *testing.T) {
	t.Parallel()

	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[1,2]}`),
	})
	snapshot, err := doc.Snapshot("checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemStore()
	if err := store.Save(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}

	snapshot.Features[0].ID = "mutated"
	snapshot.VectorClock["site-a"] = 99
	snapshot.OutboxOps[0].Geometry[0] = 'x'
	loaded, err := store.Load(context.Background(), "test-document", "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Features[0].ID != "f" || loaded.VectorClock["site-a"] != 1 ||
		!json.Valid(loaded.OutboxOps[0].Geometry) {
		t.Fatalf("save retained caller aliases: %+v", loaded)
	}

	loaded.Features[0].ID = "mutated-again"
	loaded.VectorClock["site-a"] = 100
	loadedAgain, err := store.Load(context.Background(), "test-document", "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if loadedAgain.Features[0].ID != "f" || loadedAgain.VectorClock["site-a"] != 1 {
		t.Fatalf("load returned store-owned aliases: %+v", loadedAgain)
	}
}
