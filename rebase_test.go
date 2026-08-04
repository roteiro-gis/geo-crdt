package crdt

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestDocumentRebaseStartsCompactNamespacedEpoch(t *testing.T) {
	doc, err := NewDocumentFromFeatureCollection("parcels-v1", "site-a", json.RawMessage(`{
		"type":"FeatureCollection",
		"features":[{
			"type":"Feature",
			"id":"road",
			"geometry":{"type":"LineString","coordinates":[[0,0],[2,0]]},
			"properties":{"name":"Main","obsolete":true}
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	mustApply(t, doc, SetGeometry{
		FeatureID: "road",
		Geometry:  json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[2,0]]}`),
	})
	info, ok := doc.GeometryInfo("road")
	if !ok {
		t.Fatal("road geometry missing")
	}
	partID := info[0].ID
	ringID := info[0].Rings[0].ID
	afterID := info[0].Rings[0].Vertices[0].ID
	mustApply(t, doc, InsertFeatureVertex{
		FeatureID:     "road",
		PartID:        partID,
		RingID:        ringID,
		AfterVertexID: afterID,
		Coord:         Coord{X: 1, Y: 0},
	})
	info, _ = doc.GeometryInfo("road")
	insertedID := info[0].Rings[0].Vertices[1].ID
	mustApply(t, doc, DeleteFeatureVertex{
		FeatureID: "road",
		PartID:    partID,
		RingID:    ringID,
		VertexID:  insertedID,
	})
	mustApply(t, doc, SetProperty{FeatureID: "road", Key: "temporary", Value: 1})
	mustApply(t, doc, DeleteProperty{FeatureID: "road", Key: "temporary"})
	mustApply(t, doc, DeleteProperty{FeatureID: "road", Key: "obsolete"})
	mustApply(t, doc, InsertFeature{
		FeatureID:  "discarded",
		Geometry:   json.RawMessage(`{"type":"Point","coordinates":[5,5]}`),
		Properties: map[string]any{"keep": false},
	})
	mustApply(t, doc, DeleteFeature{FeatureID: "discarded"})

	before, err := doc.FeatureCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	oldDelta := doc.DeltaSince(nil)
	if len(oldDelta.Ops) == 0 || len(doc.seen) == 0 {
		t.Fatal("test setup did not create retained history")
	}
	if _, exists := doc.features["discarded"]; !exists {
		t.Fatal("test setup did not create a feature tombstone")
	}
	if _, exists := doc.features["road"].properties["temporary"]; !exists {
		t.Fatal("test setup did not create a property tombstone")
	}

	_, watermark := doc.PendingOps()
	if err := doc.MarkSynced(watermark); err != nil {
		t.Fatal(err)
	}
	stable := doc.VectorClock()
	receipt, err := doc.Rebase("parcels-v2", stable, "epoch-v2")
	if err != nil {
		t.Fatal(err)
	}

	if receipt.PreviousDocumentID != "parcels-v1" || receipt.DocumentID != "parcels-v2" {
		t.Fatalf("unexpected receipt document IDs: %+v", receipt)
	}
	if receipt.PreviousBaseHash == receipt.BaseHash {
		t.Fatal("new epoch retained the previous base hash")
	}
	if !reflect.DeepEqual(receipt.StableClock, stable) {
		t.Fatalf("stable clock = %v, want %v", receipt.StableClock, stable)
	}
	if receipt.Snapshot.ID != "epoch-v2" || receipt.Snapshot.DocumentID != "parcels-v2" {
		t.Fatalf("unexpected rebase snapshot: %+v", receipt.Snapshot)
	}
	if doc.DocumentID() != "parcels-v2" || doc.Clock() != 0 {
		t.Fatalf("document did not reset into new epoch: id=%q clock=%d", doc.DocumentID(), doc.Clock())
	}
	if len(doc.ops) != 0 || len(doc.seen) != 0 || len(doc.compacted) != 0 ||
		len(doc.frontier.frontier) != 0 || len(doc.frontier.staged) != 0 ||
		len(doc.pendingGen) != 0 || doc.localSeq != 0 ||
		doc.syncedThrough != 0 || doc.issuedThrough != 0 {
		t.Fatal("rebase retained operation, clock, outbox, or dependency history")
	}
	if len(doc.features) != 1 {
		t.Fatalf("rebase retained %d features, want one visible feature", len(doc.features))
	}
	road := doc.features["road"]
	if road == nil || !road.isBase || road.createReg.isSet() || road.deleteReg.isSet() ||
		road.genID.isSet() || road.genStamp.isSet() || len(road.seenGens) != 1 {
		t.Fatalf("road did not become clean base state: %+v", road)
	}
	if _, exists := road.properties["temporary"]; exists {
		t.Fatal("rebase retained deleted property register")
	}
	if _, exists := road.properties["obsolete"]; exists {
		t.Fatal("rebase retained deleted base property")
	}
	if got := len(road.geometry.parts[0].rings[0].seq.byID); got != 2 {
		t.Fatalf("rebase retained vertex tombstones: got %d vertices, want 2", got)
	}

	after, err := doc.FeatureCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("visible state changed across rebase\nbefore: %s\nafter:  %s", before, after)
	}

	restored, err := NewDocumentFromSnapshot("site-b", receipt.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredJSON, err := restored.FeatureCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredJSON) != string(after) {
		t.Fatalf("rebase snapshot changed state\nwant: %s\ngot:  %s", after, restoredJSON)
	}
	if _, err := doc.MergeDelta(oldDelta); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("old-epoch delta error = %v, want ErrDocumentMismatch", err)
	}

	mustApply(t, doc, SetProperty{FeatureID: "road", Key: "speed", Value: 25})
	pending, nextWatermark := doc.PendingOps()
	if len(pending) != 1 || pending[0].Seq != 1 || nextWatermark != 1 {
		t.Fatalf("new epoch outbox = (%+v, %d), want one operation at sequence 1", pending, nextWatermark)
	}
}

func TestDocumentRebaseRejectsUnsafeCutsWithoutMutation(t *testing.T) {
	t.Run("unacknowledged outbox", func(t *testing.T) {
		doc := NewDocument("document-v1", "site-a")
		mustApply(t, doc, InsertFeature{FeatureID: "point", Geometry: json.RawMessage(
			`{"type":"Point","coordinates":[1,2]}`)})
		assertRejectedRebaseUnchanged(t, doc, doc.VectorClock())
	})

	t.Run("stable clock mismatch", func(t *testing.T) {
		doc := NewDocument("document-v1", "site-a")
		mustApply(t, doc, InsertFeature{FeatureID: "point"})
		_, watermark := doc.PendingOps()
		if err := doc.MarkSynced(watermark); err != nil {
			t.Fatal(err)
		}
		assertRejectedRebaseUnchanged(t, doc, VectorClock{})
	})

	t.Run("delivery gap", func(t *testing.T) {
		source := NewDocument("document-v1", "site-a")
		mustApply(t, source, InsertFeature{FeatureID: "point"})
		mustApply(t, source, SetProperty{FeatureID: "point", Key: "one", Value: 1})
		mustApply(t, source, SetProperty{FeatureID: "point", Key: "three", Value: 3})

		receiver := NewDocument("document-v1", "site-b")
		ops := source.Ops()
		if _, err := receiver.MergeOps("document-v1", []DocumentOp{ops[0], ops[2]}); err != nil {
			t.Fatal(err)
		}
		assertRejectedRebaseUnchanged(t, receiver, receiver.VectorClock())
	})

	t.Run("unresolved dependency", func(t *testing.T) {
		doc, err := NewDocumentFromFeatureCollection("document-v1", "site-a", json.RawMessage(`{
			"type":"FeatureCollection",
			"features":[{
				"type":"Feature",
				"id":"line",
				"geometry":{"type":"LineString","coordinates":[[0,0],[1,1]]},
				"properties":{}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		op := DocumentOp{
			Type:      OpEditGeometry,
			SiteID:    "site-b",
			Seq:       1,
			Timestamp: 1,
			FeatureID: "line",
			GeometryOp: &GeometryOp{
				Action:   ActionMoveVertex,
				PartID:   InitialPartID(0),
				RingID:   InitialRingID(0, 0),
				VertexID: "missing",
				Coord:    []float64{2, 2},
			},
		}
		if result, err := doc.MergeOps("document-v1", []DocumentOp{op}); err != nil {
			t.Fatal(err)
		} else if len(result.Buffered) != 1 {
			t.Fatalf("merge result = %+v, want buffered dependency", result)
		}
		assertRejectedRebaseUnchanged(t, doc, doc.VectorClock())
	})

	t.Run("namespace not replaced", func(t *testing.T) {
		doc := NewDocument("document-v1", "site-a")
		beforeHash := doc.BaseHash()
		if _, err := doc.Rebase(doc.DocumentID(), doc.VectorClock(), "rejected"); !errors.Is(err, ErrRebaseUnsafe) {
			t.Fatalf("Rebase error = %v, want ErrRebaseUnsafe", err)
		}
		if doc.DocumentID() != "document-v1" || doc.BaseHash() != beforeHash {
			t.Fatal("failed rebase mutated document")
		}
	})
}

func assertRejectedRebaseUnchanged(t *testing.T, doc *Document, stable VectorClock) {
	t.Helper()
	beforeID := doc.DocumentID()
	beforeHash := doc.BaseHash()
	beforeOps := doc.Ops()
	beforeJSON, jsonErr := doc.FeatureCollectionJSON()
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}

	target := DocumentID("document-v2")
	if beforeID == target {
		target = beforeID
	}
	if _, err := doc.Rebase(target, stable, "rejected"); !errors.Is(err, ErrRebaseUnsafe) {
		t.Fatalf("Rebase error = %v, want ErrRebaseUnsafe", err)
	}
	afterJSON, jsonErr := doc.FeatureCollectionJSON()
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if doc.DocumentID() != beforeID || doc.BaseHash() != beforeHash ||
		!reflect.DeepEqual(doc.Ops(), beforeOps) || string(afterJSON) != string(beforeJSON) {
		t.Fatal("failed rebase mutated document")
	}
}
