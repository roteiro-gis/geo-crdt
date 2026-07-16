// Two replicas load the same GeoJSON FeatureCollection, edit concurrently,
// and exchange deltas.
package main

import (
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func main() {
	base := json.RawMessage(`{
		"type":"FeatureCollection",
		"features":[{
			"type":"Feature",
			"id":"parcel-123",
			"geometry":{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]},
			"properties":{"owner":"Smith"}
		}]
	}`)

	siteA, err := crdt.NewDocumentFromFeatureCollection("site-a", base)
	if err != nil {
		log.Fatal(err)
	}
	siteB, err := crdt.NewDocumentFromFeatureCollection("site-b", base)
	if err != nil {
		log.Fatal(err)
	}

	if err := siteA.Apply(crdt.SetProperty{
		FeatureID: "parcel-123",
		Key:       "owner",
		Value:     "Jones",
	}); err != nil {
		log.Fatal(err)
	}

	if err := siteB.Apply(crdt.MoveFeatureVertex{
		FeatureID: "parcel-123",
		PartID:    crdt.InitialPartID(0),
		RingID:    crdt.InitialRingID(0, 0),
		VertexID:  crdt.InitialVertexID(0, 2),
		Coord:     crdt.Coord{X: 11, Y: 11},
	}); err != nil {
		log.Fatal(err)
	}

	if _, err := siteA.MergeDelta(siteB.DeltaSince(siteA.VectorClock())); err != nil {
		log.Fatal(err)
	}
	if _, err := siteB.MergeDelta(siteA.DeltaSince(siteB.VectorClock())); err != nil {
		log.Fatal(err)
	}

	data, err := siteA.FeatureCollectionJSON()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}
