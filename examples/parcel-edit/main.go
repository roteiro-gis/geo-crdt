// Feature-level editing: insert a parcel, cut a hole, edit vertices and
// properties.
package main

import (
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func main() {
	doc := crdt.NewDocument("site-a")

	if err := doc.Apply(crdt.InsertFeature{
		FeatureID: "parcel-123",
		Geometry:  json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[20,0],[20,20],[0,20],[0,0]]]}`),
		Properties: map[string]any{
			"owner": "Smith",
			"zone":  "R1",
		},
	}); err != nil {
		log.Fatal(err)
	}

	// Insert a vertex after the second corner.
	if err := doc.Apply(crdt.InsertFeatureVertex{
		FeatureID:     "parcel-123",
		PartID:        crdt.InitialPartID(0),
		RingID:        crdt.InitialRingID(0, 0),
		AfterVertexID: crdt.InitialVertexID(0, 1),
		Coord:         crdt.Coord{X: 20, Y: 10},
	}); err != nil {
		log.Fatal(err)
	}

	// Cut a hole (an easement) into the parcel.
	if err := doc.Apply(crdt.AddFeatureRing{
		FeatureID: "parcel-123",
		PartID:    crdt.InitialPartID(0),
		Coords:    []crdt.Coord{{X: 5, Y: 5}, {X: 5, Y: 8}, {X: 8, Y: 8}, {X: 8, Y: 5}},
	}); err != nil {
		log.Fatal(err)
	}

	if err := doc.Apply(crdt.SetProperty{
		FeatureID: "parcel-123",
		Key:       "reviewed",
		Value:     true,
	}); err != nil {
		log.Fatal(err)
	}

	feature, ok := doc.Feature("parcel-123")
	if !ok {
		log.Fatal("parcel not found")
	}
	fmt.Println(string(feature.Geometry))
	fmt.Println(feature.Properties)
}
