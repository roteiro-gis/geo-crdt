// Two replicas concurrently edit one polygon and converge after merging.
package main

import (
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func main() {
	initial := json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`)

	siteA, err := crdt.NewGeometryCRDT("site-a", initial)
	if err != nil {
		log.Fatal(err)
	}
	siteB, err := crdt.NewGeometryCRDT("site-b", initial)
	if err != nil {
		log.Fatal(err)
	}

	part, ring := crdt.InitialPartID(0), crdt.InitialRingID(0, 0)

	// Site A moves the second vertex; site B inserts a midpoint after it.
	if err := siteA.Apply(crdt.MoveVertexOp(part, ring, crdt.InitialVertexID(0, 1), crdt.Coord{X: 8, Y: 0})); err != nil {
		log.Fatal(err)
	}
	if err := siteB.Apply(crdt.InsertVertexOp(part, ring, crdt.InitialVertexID(0, 1), crdt.Coord{X: 10, Y: 5})); err != nil {
		log.Fatal(err)
	}

	if _, err := siteA.Merge(siteB); err != nil {
		log.Fatal(err)
	}
	if _, err := siteB.Merge(siteA); err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(siteA.Geometry()))
	fmt.Println("converged:", string(siteA.Geometry()) == string(siteB.Geometry()))
}
