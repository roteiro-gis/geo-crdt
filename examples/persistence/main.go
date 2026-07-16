// Persist operations and snapshots through the storage interfaces, then
// rehydrate a replica from storage. MemStore stands in for a durable
// backend.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func main() {
	ctx := context.Background()
	store := crdt.NewMemStore()

	// An editing session persists its ops as it syncs.
	doc := crdt.NewDocument("site-a")
	if err := doc.Apply(crdt.InsertFeature{
		FeatureID:  "hydrant-7",
		Geometry:   json.RawMessage(`{"type":"Point","coordinates":[-122.4,37.7]}`),
		Properties: map[string]any{"status": "ok"},
	}); err != nil {
		log.Fatal(err)
	}
	pending, watermark := doc.PendingOps()
	if err := store.Append(ctx, pending); err != nil {
		log.Fatal(err)
	}
	doc.MarkSynced(watermark)

	// Periodic checkpoint.
	snapshot, err := doc.Snapshot("nightly")
	if err != nil {
		log.Fatal(err)
	}
	if err := store.Save(ctx, snapshot); err != nil {
		log.Fatal(err)
	}

	// More edits after the checkpoint.
	if err := doc.Apply(crdt.SetProperty{FeatureID: "hydrant-7", Key: "status", Value: "needs-service"}); err != nil {
		log.Fatal(err)
	}
	pending, watermark = doc.PendingOps()
	if err := store.Append(ctx, pending); err != nil {
		log.Fatal(err)
	}
	doc.MarkSynced(watermark)

	// Rehydrate: load the checkpoint, then replay ops beyond it.
	loaded, err := store.Load(ctx, "nightly")
	if err != nil {
		log.Fatal(err)
	}
	restored, err := crdt.NewDocumentFromSnapshot("site-b", loaded)
	if err != nil {
		log.Fatal(err)
	}
	ops, err := store.Since(ctx, restored.VectorClock())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := restored.MergeOps(ops); err != nil {
		log.Fatal(err)
	}

	data, err := restored.FeatureCollectionJSON()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}
