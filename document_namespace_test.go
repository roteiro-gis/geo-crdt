package crdt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDocumentNamespaceRejectsMisroutedState(t *testing.T) {
	t.Parallel()

	left := NewDocument("document-a", "actor")
	right := NewDocument("document-b", "actor")
	mustApply(t, left, InsertFeature{
		FeatureID: "left",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[1,1]}`),
	})
	mustApply(t, right, InsertFeature{
		FeatureID: "right",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[2,2]}`),
	})

	if _, err := right.Merge(left); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("document merge error = %v", err)
	}
	if _, err := right.MergeDelta(left.DeltaSince(nil)); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("delta merge error = %v", err)
	}
	if _, err := right.MergeOps("document-a", left.Ops()); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("operation merge error = %v", err)
	}
	if _, ok := right.Feature("left"); ok {
		t.Fatal("misrouted feature entered destination document")
	}
}

func TestEmptyDocumentIDsCreateDistinctNamespaces(t *testing.T) {
	t.Parallel()

	left := NewDocument("", "left")
	right := NewDocument("", "right")
	if left.DocumentID() == "" || right.DocumentID() == "" {
		t.Fatal("empty constructor input did not create a document namespace")
	}
	if left.DocumentID() == right.DocumentID() {
		t.Fatal("fresh documents unexpectedly share a namespace")
	}
	if _, err := left.Merge(right); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("fresh document merge error = %v", err)
	}
}

func TestMemStoreMultiplexesDocumentNamespaces(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemStore()
	opA := DocumentOp{
		Type: OpInsertFeature, SiteID: "actor", Seq: 1, Timestamp: 1,
		FeatureID: "a",
	}
	opB := opA
	opB.FeatureID = "b"
	if err := store.Append(ctx, "document-a", []DocumentOp{opA}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, "document-b", []DocumentOp{opB}); err != nil {
		t.Fatalf("same operation identity in another document collided: %v", err)
	}
	gotA, err := store.Since(ctx, "document-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := store.Since(ctx, "document-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotA) != 1 || gotA[0].FeatureID != "a" || len(gotB) != 1 || gotB[0].FeatureID != "b" {
		t.Fatalf("document operation keys crossed: A=%+v B=%+v", gotA, gotB)
	}

	docA := NewDocument("document-a", "a")
	docB := NewDocument("document-b", "b")
	snapshotA, err := docA.Snapshot("checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	snapshotB, err := docB.Snapshot("checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, snapshotA); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, snapshotB); err != nil {
		t.Fatal(err)
	}
	loadedA, err := store.Load(ctx, "document-a", "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	loadedB, err := store.Load(ctx, "document-b", "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if loadedA.DocumentID != "document-a" || loadedB.DocumentID != "document-b" {
		t.Fatalf("snapshot keys crossed: A=%q B=%q", loadedA.DocumentID, loadedB.DocumentID)
	}
}
