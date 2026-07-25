// Checkpoint a document as a snapshot, restore a replica from it, and catch
// up with a delta.
package main

import (
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func main() {
	siteA := crdt.NewDocument("test-document", "site-a")
	if err := siteA.Apply(crdt.InsertFeature{
		FeatureID:  "trail-1",
		Geometry:   json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`),
		Properties: map[string]any{"name": "North Trail"},
	}); err != nil {
		log.Fatal(err)
	}

	// Snapshots carry full CRDT state (registers, tombstones, stable IDs),
	// so a restored replica keeps merging deltas.
	snapshot, err := siteA.Snapshot("checkpoint-1")
	if err != nil {
		log.Fatal(err)
	}
	siteB, err := crdt.NewDocumentFromSnapshot("site-b", snapshot)
	if err != nil {
		log.Fatal(err)
	}

	if err := siteA.Apply(crdt.MoveFeatureVertex{
		FeatureID: "trail-1",
		PartID:    crdt.InitialPartID(0),
		RingID:    crdt.InitialRingID(0, 0),
		VertexID:  crdt.InitialVertexID(0, 1),
		Coord:     crdt.Coord{X: 2, Y: 2},
	}); err != nil {
		log.Fatal(err)
	}

	if _, err := siteB.MergeDelta(siteA.DeltaSince(siteB.VectorClock())); err != nil {
		log.Fatal(err)
	}

	data, err := siteB.FeatureCollectionJSON()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}
