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

	left := NewDocument("left")
	right := NewDocument("right")
	if _, err := left.MergeOps([]DocumentOp{first, second}); err != nil {
		t.Fatal(err)
	}
	if _, err := right.MergeOps([]DocumentOp{second, first}); err != nil {
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

func TestIdentityReuseWithDifferentPayloadIsRejectedAtomically(t *testing.T) {
	t.Parallel()

	first := DocumentOp{
		Type: OpInsertFeature, SiteID: "actor", Seq: 1, Timestamp: 1,
		FeatureID: "first",
	}
	collision := first
	collision.FeatureID = "second"

	doc := NewDocument("local")
	if _, err := doc.MergeOps([]DocumentOp{first, collision}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("collision error = %v", err)
	}
	if len(doc.Ops()) != 0 {
		t.Fatalf("colliding batch partially applied: %#v", doc.Ops())
	}

	store := NewMemStore()
	if err := store.Append(context.Background(), []DocumentOp{first}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), []DocumentOp{collision}); !errors.Is(err, ErrIdentityCollision) {
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

	doc := NewDocument("local")
	result, err := doc.MergeOps([]DocumentOp{first, same})
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", result.Duplicates)
	}
}
