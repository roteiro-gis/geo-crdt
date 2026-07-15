// PendingOps/MarkSynced watermark flow over a JSON wire.
package main

import (
	"encoding/json"
	"fmt"
	"log"

	crdt "github.com/i-norden/geo-crdt"
)

func main() {
	initial := json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[2,2]]}`)

	siteA, err := crdt.NewGeometryCRDT("site-a", initial)
	if err != nil {
		log.Fatal(err)
	}
	siteB, err := crdt.NewGeometryCRDT("site-b", initial)
	if err != nil {
		log.Fatal(err)
	}

	op := crdt.InsertVertexOp(crdt.InitialPartID(0), crdt.InitialRingID(0, 0), crdt.InitialVertexID(0, 0), crdt.Coord{X: 1, Y: 1})
	if err := siteA.Apply(op); err != nil {
		log.Fatal(err)
	}

	// Ship pending ops as JSON; acknowledge with the watermark only after
	// the send succeeded. Ops applied in between stay pending.
	pending, watermark := siteA.PendingOps()
	wire, err := json.Marshal(pending)
	if err != nil {
		log.Fatal(err)
	}

	var received []crdt.GeometryOp
	if err := json.Unmarshal(wire, &received); err != nil {
		log.Fatal(err)
	}
	if _, err := siteB.MergeOps(received); err != nil {
		log.Fatal(err)
	}
	siteA.MarkSynced(watermark)

	fmt.Println(string(siteB.Geometry()))
}
