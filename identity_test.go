package crdt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestLWWTotalOrderIncludesActorSequence(t *testing.T) {
	t.Parallel()

	first := DocumentOp{
		Type: OpInsertFeature, SiteID: "actor", Seq: 1, Timestamp: 7,
		FeatureID: "feature", Properties: map[string]json.RawMessage{"value": json.RawMessage(`"first"`)},
	}
	second := DocumentOp{
		Type: OpInsertFeature, SiteID: "actor", Seq: 2, Timestamp: 7,
		FeatureID: "feature", Properties: map[string]json.RawMessage{"value": json.RawMessage(`"second"`)},
	}

	left := NewDocument("test-document", "left")
	right := NewDocument("test-document", "right")
	if _, err := left.MergeOps("test-document", []DocumentOp{first, second}); err != nil {
		t.Fatal(err)
	}
	if _, err := right.MergeOps("test-document", []DocumentOp{second, first}); err != nil {
		t.Fatal(err)
	}
	leftJSON, err := left.FeatureCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := right.FeatureCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("delivery order changed state:\nleft  %s\nright %s", leftJSON, rightJSON)
	}
	feature, ok := left.Feature("feature")
	if !ok || feature.Properties["value"] != "second" {
		t.Fatalf("sequence 2 did not win equal Lamport values: %#v", feature)
	}
}

func TestCreatedElementIDsAreDerived(t *testing.T) {
	t.Parallel()

	replica, err := NewGeometryCRDT("local", json.RawMessage(
		`{"type":"LineString","coordinates":[[0,0],[1,1]]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = replica.MergeOps([]GeometryOp{{
		Action: ActionInsertVertex, SiteID: "actor", Seq: 1, Timestamp: 1,
		PartID: InitialPartID(0), RingID: InitialRingID(0, 0),
		VertexID: "caller-selected", AfterVertexID: InitialVertexID(0, 0),
		Coord: []float64{0.5, 0.5},
	}})
	if !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("caller-selected vertex ID error = %v", err)
	}
}

func TestAddedRingOrderIncludesActorSequence(t *testing.T) {
	t.Parallel()

	initial := json.RawMessage(
		`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,0]]]}`,
	)
	left, err := NewGeometryCRDT("left", initial)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewGeometryCRDT("right", initial)
	if err != nil {
		t.Fatal(err)
	}
	first := GeometryOp{
		Action: ActionAddRing, SiteID: "actor", Seq: 1, Timestamp: 7,
		PartID: InitialPartID(0),
		Ring:   [][]float64{{1, 1}, {2, 1}, {1, 2}},
	}
	second := GeometryOp{
		Action: ActionAddRing, SiteID: "actor", Seq: 2, Timestamp: 7,
		PartID: InitialPartID(0),
		Ring:   [][]float64{{3, 3}, {4, 3}, {3, 4}},
	}
	if _, err := left.MergeOps([]GeometryOp{first}); err != nil {
		t.Fatal(err)
	}
	if _, err := left.MergeOps([]GeometryOp{second}); err != nil {
		t.Fatal(err)
	}
	if _, err := right.MergeOps([]GeometryOp{second}); err != nil {
		t.Fatal(err)
	}
	if _, err := right.MergeOps([]GeometryOp{first}); err != nil {
		t.Fatal(err)
	}
	if string(left.Geometry()) != string(right.Geometry()) {
		t.Fatalf("added ring order depends on arrival:\nleft  %s\nright %s", left.Geometry(), right.Geometry())
	}
}

func TestIdentityReuseWithDifferentPayloadIsRejectedAtomically(t *testing.T) {
	t.Parallel()

	first := DocumentOp{
		Type: OpInsertFeature, SiteID: "actor", Seq: 1, Timestamp: 1,
		FeatureID: "first",
	}
	collision := first
	collision.FeatureID = "second"

	doc := NewDocument("test-document", "local")
	if _, err := doc.MergeOps("test-document", []DocumentOp{first, collision}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("collision error = %v", err)
	}
	if len(doc.Ops()) != 0 {
		t.Fatalf("colliding batch partially applied: %#v", doc.Ops())
	}

	store := NewMemStore()
	if err := store.Append(context.Background(), "test-document", []DocumentOp{first}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), "test-document", []DocumentOp{collision}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("store collision error = %v", err)
	}
}

func TestIdentityDigestUsesCanonicalJSON(t *testing.T) {
	t.Parallel()

	first := DocumentOp{
		Type: OpSetProperty, SiteID: "actor", Seq: 1, Timestamp: 1,
		FeatureID: "feature", PropertyKey: "value",
		PropertyValue: json.RawMessage(`{"a":1,"b":2}`),
	}
	same := first
	same.PropertyValue = json.RawMessage(`{ "b": 2, "a": 1 }`)

	doc := NewDocument("test-document", "local")
	result, err := doc.MergeOps("test-document", []DocumentOp{first, same})
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", result.Duplicates)
	}
}
