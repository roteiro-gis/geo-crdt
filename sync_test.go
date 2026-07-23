package crdt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// Snapshots are full-fidelity: registers, tombstones, and stable vertex IDs
// survive, so a restored replica keeps syncing correctly (the old design lost
// vertex identity across snapshots).
func TestSnapshotRoundTripPreservesCRDTState(t *testing.T) {
	source := newFCDocument(t, "site-a")
	mustApply(t, source, SetProperty{FeatureID: "parcel-1", Key: "owner", Value: "Jones"})
	mustApply(t, source, DeleteProperty{FeatureID: "parcel-1", Key: "zone"})
	mustApply(t, source, MoveFeatureVertex{
		FeatureID: "parcel-1",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 0),
		VertexID:  InitialVertexID(0, 1),
		Coord:     Coord{X: 8, Y: 0},
	})
	mustApply(t, source, InsertFeatureVertex{
		FeatureID:     "parcel-1",
		PartID:        InitialPartID(0),
		RingID:        InitialRingID(0, 0),
		AfterVertexID: InitialVertexID(0, 1),
		Coord:         Coord{X: 9, Y: 5},
	})

	snapshot, err := source.Snapshot("checkpoint-1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Snapshots must survive JSON transport.
	wire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}

	loaded, err := NewDocumentFromSnapshot("site-b", decoded)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if a, b := documentJSON(t, source), documentJSON(t, loaded); a != b {
		t.Fatalf("restored state mismatch:\nsource  %s\nrestored %s", a, b)
	}

	// Continued sync: edits against pre-snapshot stable vertex IDs and the
	// inserted vertex must apply on the restored replica.
	insertedID, err := source.VertexIDAt("parcel-1", InitialPartID(0), InitialRingID(0, 0), 2)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, source, MoveFeatureVertex{
		FeatureID: "parcel-1",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 0),
		VertexID:  insertedID,
		Coord:     Coord{X: 9, Y: 6},
	})
	mustApply(t, source, SetProperty{FeatureID: "parcel-1", Key: "owner", Value: "Final"})

	if _, err := loaded.MergeDelta(source.DeltaSince(loaded.VectorClock())); err != nil {
		t.Fatalf("post-snapshot delta: %v", err)
	}
	if a, b := documentJSON(t, source), documentJSON(t, loaded); a != b {
		t.Fatalf("post-snapshot sync mismatch:\nsource  %s\nrestored %s", a, b)
	}
}

func TestSnapshotPreservesDeletedFeatureTombstones(t *testing.T) {
	source := NewDocument("site-a")
	mustApply(t, source, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	mustApply(t, source, DeleteFeature{FeatureID: "f"})

	snapshot, err := source.Snapshot("cp")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := NewDocumentFromSnapshot("site-b", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Feature("f"); ok {
		t.Fatal("deleted feature resurrected by snapshot load")
	}
	// A newer insert still resurrects.
	mustApply(t, loaded, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[9,9]}`),
	})
	if _, ok := loaded.Feature("f"); !ok {
		t.Fatal("resurrecting insert failed after snapshot load")
	}
}

// Operations buffered on missing dependencies survive snapshots and drain
// after restore.
func TestSnapshotCarriesPendingOps(t *testing.T) {
	source := NewDocument("site-a")
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

	// A replica receives only the edit; the defining insert is missing.
	partial := NewDocument("site-b")
	result, err := partial.MergeOps(ops[1:])
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Buffered) != 1 {
		t.Fatalf("expected buffered edit, got %+v", result)
	}

	snapshot, err := partial.Snapshot("cp")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PendingOps) != 1 {
		t.Fatalf("expected pending op in snapshot, got %d", len(snapshot.PendingOps))
	}

	restored, err := NewDocumentFromSnapshot("site-c", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// The missing insert arrives; the buffered edit drains.
	if _, err := restored.MergeOps(ops[:1]); err != nil {
		t.Fatal(err)
	}
	if a, b := documentJSON(t, source), documentJSON(t, restored); a != b {
		t.Fatalf("pending op lost across snapshot:\nsource   %s\nrestored %s", a, b)
	}
}

// Regression (review finding A5): restoring a site's own identity resumes
// sequence numbering above everything the snapshot folded.
func TestSnapshotRestoreResumesOwnNumbering(t *testing.T) {
	source := NewDocument("site-a")
	mustApply(t, source, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	mustApply(t, source, SetProperty{FeatureID: "f", Key: "k", Value: 1})

	snapshot, err := source.Snapshot("cp")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewDocumentFromSnapshot("site-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, restored, SetProperty{FeatureID: "f", Key: "k", Value: 2})

	pending, _ := restored.PendingOps()
	if len(pending) != 1 || pending[0].Seq != 3 {
		t.Fatalf("restored numbering must continue after folded ops, got %+v", pending)
	}
}

func TestSnapshotVersionAndLineageChecks(t *testing.T) {
	source := NewDocument("site-a")
	snapshot, err := source.Snapshot("cp")
	if err != nil {
		t.Fatal(err)
	}

	bad := snapshot
	bad.Version = 1
	if _, err := NewDocumentFromSnapshot("site-b", bad); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("expected ErrUnsupportedVersion, got %v", err)
	}

	delta := source.DeltaSince(nil)
	delta.Version = 1
	if _, err := source.MergeDelta(delta); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("expected ErrUnsupportedVersion, got %v", err)
	}
}

// A replica that never saw the compacted history must load a snapshot; a
// delta cannot bridge the gap.
func TestCompactionGapDetected(t *testing.T) {
	source := NewDocument("site-a")
	mustApply(t, source, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	snapshot, err := source.Snapshot("cp")
	if err != nil {
		t.Fatal(err)
	}

	compacted, err := NewDocumentFromSnapshot("site-b", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, compacted, SetProperty{FeatureID: "f", Key: "k", Value: 1})

	fresh := NewDocument("site-c")
	if _, err := fresh.MergeDelta(compacted.DeltaSince(fresh.VectorClock())); !errors.Is(err, ErrCompactionGap) {
		t.Fatalf("expected ErrCompactionGap, got %v", err)
	}
	if _, err := fresh.Merge(compacted); !errors.Is(err, ErrCompactionGap) {
		t.Fatalf("expected ErrCompactionGap via Merge, got %v", err)
	}

	// Loading the snapshot bridges the gap.
	caughtUp, err := NewDocumentFromSnapshot("site-c", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caughtUp.MergeDelta(compacted.DeltaSince(caughtUp.VectorClock())); err != nil {
		t.Fatalf("post-snapshot delta: %v", err)
	}
	if a, b := documentJSON(t, compacted), documentJSON(t, caughtUp); a != b {
		t.Fatalf("catch-up mismatch:\n%s\n%s", a, b)
	}
}

// The vector clock only advances through contiguous sequence prefixes, so a
// dropped op is always re-requested and sync self-heals.
func TestDeliveryGapSelfHeals(t *testing.T) {
	source := NewDocument("site-a")
	mustApply(t, source, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	mustApply(t, source, SetProperty{FeatureID: "f", Key: "a", Value: 1})
	mustApply(t, source, SetProperty{FeatureID: "f", Key: "b", Value: 2})
	ops := source.Ops()

	receiver := NewDocument("site-b")
	// Ops 1 and 3 arrive; op 2 is lost in transit.
	if _, err := receiver.MergeOps([]DocumentOp{ops[0], ops[2]}); err != nil {
		t.Fatal(err)
	}
	if got := receiver.VectorClock()["site-a"]; got != 1 {
		t.Fatalf("frontier advanced past a gap: %d", got)
	}

	// A vector-clock delta re-delivers the gap (and the staged op, which
	// dedups).
	result, err := receiver.MergeDelta(source.DeltaSince(receiver.VectorClock()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 || result.Duplicates != 1 {
		t.Fatalf("expected gap fill + duplicate, got %+v", result)
	}
	if got := receiver.VectorClock()["site-a"]; got != 3 {
		t.Fatalf("frontier should cover all ops, got %d", got)
	}
	if a, b := documentJSON(t, source), documentJSON(t, receiver); a != b {
		t.Fatalf("self-heal failed:\n%s\n%s", a, b)
	}
}

func TestSnapshotRetainsEveryOperationBeyondDeliveryGap(t *testing.T) {
	t.Parallel()

	insert := DocumentOp{
		Type: OpInsertFeature, SiteID: "actor", Seq: 1, Timestamp: 1,
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	}
	applied := DocumentOp{
		Type: OpSetProperty, SiteID: "actor", Seq: 3, Timestamp: 3,
		FeatureID: "f", PropertyKey: "value", PropertyValue: json.RawMessage(`3`),
	}
	superseded := DocumentOp{
		Type: OpSetProperty, SiteID: "actor", Seq: 4, Timestamp: 2,
		FeatureID: "f", PropertyKey: "value", PropertyValue: json.RawMessage(`2`),
	}
	quarantined := DocumentOp{
		Type: OpSetGeometry, SiteID: "actor", Seq: 5, Timestamp: 5,
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[]}`),
	}

	source := NewDocument("source")
	if _, err := source.MergeOps([]DocumentOp{insert, applied}); err != nil {
		t.Fatal(err)
	}
	if result, err := source.MergeOps([]DocumentOp{superseded}); err != nil {
		t.Fatal(err)
	} else if len(result.Superseded) != 1 {
		t.Fatalf("expected superseded operation, got %+v", result)
	}
	if result, err := source.MergeOps([]DocumentOp{quarantined}); err != nil {
		t.Fatal(err)
	} else if len(result.Quarantined) != 1 {
		t.Fatalf("expected quarantined operation, got %+v", result)
	}
	if got := source.VectorClock()["actor"]; got != 1 {
		t.Fatalf("frontier advanced past missing sequence 2: %d", got)
	}

	snapshot, err := source.Snapshot("gap")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RetainedOps) != 3 {
		t.Fatalf("retained operations = %d, want 3", len(snapshot.RetainedOps))
	}
	restored, err := NewDocumentFromSnapshot("restored", snapshot)
	if err != nil {
		t.Fatal(err)
	}

	peer := NewDocument("peer")
	if _, err := peer.MergeOps([]DocumentOp{insert}); err != nil {
		t.Fatal(err)
	}
	delta := restored.DeltaSince(peer.VectorClock())
	if len(delta.Ops) != 3 {
		t.Fatalf("restored delta contains %d sparse operations, want 3", len(delta.Ops))
	}
	if _, err := peer.MergeDelta(delta); err != nil {
		t.Fatal(err)
	}
	if a, b := documentJSON(t, restored), documentJSON(t, peer); a != b {
		t.Fatalf("restored replica lost syncable sparse history:\n%s\n%s", a, b)
	}
}

func TestMemStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()

	source := NewDocument("site-a")
	mustApply(t, source, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[0,0]}`),
	})
	pending, watermark := source.PendingOps()
	if err := store.Append(ctx, pending); err != nil {
		t.Fatal(err)
	}
	source.MarkSynced(watermark)

	snapshot, err := source.Snapshot("cp-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	// Rehydrate: snapshot + ops beyond it.
	loadedSnapshot, err := store.Load(ctx, "cp-1")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewDocumentFromSnapshot("site-b", loadedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := store.Since(ctx, restored.VectorClock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.MergeOps(ops); err != nil {
		t.Fatal(err)
	}
	if a, b := documentJSON(t, source), documentJSON(t, restored); a != b {
		t.Fatalf("store round trip mismatch:\n%s\n%s", a, b)
	}

	// Duplicate appends are ignored.
	if err := store.Append(ctx, pending); err != nil {
		t.Fatal(err)
	}
	all, err := store.Since(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 stored op, got %d", len(all))
	}
}
