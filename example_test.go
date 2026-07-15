package crdt_test

import (
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func ExampleGeometryCRDT() {
	initial := json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[2,2]]}`)

	siteA, err := crdt.NewGeometryCRDT("site-a", initial)
	if err != nil {
		log.Fatal(err)
	}
	siteB, err := crdt.NewGeometryCRDT("site-b", initial)
	if err != nil {
		log.Fatal(err)
	}

	op := crdt.InsertVertexOp("part:0", "ring:0:0", crdt.InitialVertexID(0, 0), crdt.Coord{X: 1, Y: 1})
	if err := siteA.Apply(op); err != nil {
		log.Fatal(err)
	}

	pending, watermark := siteA.PendingOps()
	if _, err := siteB.MergeOps(pending); err != nil {
		log.Fatal(err)
	}
	siteA.MarkSynced(watermark)

	fmt.Println(string(siteB.Geometry()))
	// Output:
	// {"type":"LineString","coordinates":[[0,0],[1,1],[2,2]]}
}

func ExampleDocument() {
	doc := crdt.NewDocument("site-a")

	err := doc.Apply(crdt.InsertFeature{
		FeatureID:  "parcel-123",
		Geometry:   json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`),
		Properties: map[string]any{"owner": "Smith"},
	})
	if err != nil {
		log.Fatal(err)
	}

	err = doc.Apply(crdt.MoveFeatureVertex{
		FeatureID: "parcel-123",
		PartID:    crdt.InitialPartID(0),
		RingID:    crdt.InitialRingID(0, 0),
		VertexID:  crdt.InitialVertexID(0, 2),
		Coord:     crdt.Coord{X: 11, Y: 11},
	})
	if err != nil {
		log.Fatal(err)
	}

	feature, _ := doc.Feature("parcel-123")
	fmt.Println(string(feature.Geometry))
	// Output:
	// {"type":"Polygon","coordinates":[[[0,0],[10,0],[11,11],[0,10],[0,0]]]}
}

func ExampleValidatePolygonRings() {
	polygon := [][][]float64{
		{
			{0, 0},
			{1, 0},
			{1, 1},
			{0, 1},
			{0, 0},
		},
	}
	if err := crdt.ValidatePolygonRings(polygon); err != nil {
		log.Fatal(err)
	}
	fmt.Println("valid")
	// Output:
	// valid
}
