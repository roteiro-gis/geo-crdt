package crdt

import (
	"encoding/json"
	"fmt"
	"testing"
)

// BenchmarkInteractiveEditingSession measures the cost of a long sequence of
// local edits — the path the old replay architecture made quadratic.
func BenchmarkInteractiveEditingSession(b *testing.B) {
	initial := makePolygonJSON(squareCoords())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newBenchmarkCRDT(b, "site-a", initial)
		after := InitialVertexID(0, 0)
		for step := 0; step < 2500; step++ {
			op := InsertVertexOp(part0, ring0, after, Coord{X: float64(step), Y: float64(step % 97)})
			if err := c.Apply(op); err != nil {
				b.Fatal(err)
			}
			after = InsertedVertexID("site-a", uint64(step+1))
		}
	}
}

func BenchmarkLargePolygonMoves(b *testing.B) {
	initial := makePolygonJSON(largeRing(5000))
	ops := make([]GeometryOp, 0, 1000)
	for i := 0; i < 1000; i++ {
		ops = append(ops, GeometryOp{
			Action:    ActionMoveVertex,
			SiteID:    "site-b",
			Seq:       uint64(i + 1),
			Timestamp: uint64(i + 1),
			PartID:    part0,
			RingID:    ring0,
			VertexID:  InitialVertexID(0, i%5000),
			Coord:     []float64{float64(i), float64(i % 97)},
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newBenchmarkCRDT(b, "site-a", initial)
		if _, err := c.MergeOps(ops); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLongOpLogMerge(b *testing.B) {
	initial := makePolygonJSON(squareCoords())
	remote := newBenchmarkCRDT(b, "site-b", initial)
	after := InitialVertexID(0, 0)
	for i := 0; i < 2500; i++ {
		if err := remote.Apply(InsertVertexOp(part0, ring0, after, Coord{X: float64(i), Y: float64(i)})); err != nil {
			b.Fatal(err)
		}
		after = InsertedVertexID("site-b", uint64(i+1))
	}
	ops := remote.Ops()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newBenchmarkCRDT(b, "site-a", initial)
		if _, err := c.MergeOps(ops); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkManyReplicasMerge(b *testing.B) {
	initial := makePolygonJSON(squareCoords())
	var ops []GeometryOp
	for replica := 0; replica < 25; replica++ {
		c := newBenchmarkCRDT(b, fmt.Sprintf("site-%d", replica), initial)
		for step := 0; step < 20; step++ {
			info := c.Info()
			vertices := info[0].Rings[0].Vertices
			after := vertices[step%len(vertices)].ID
			if err := c.Apply(InsertVertexOp(part0, ring0, after, Coord{X: float64(replica), Y: float64(step)})); err != nil {
				b.Fatal(err)
			}
		}
		ops = append(ops, c.Ops()...)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newBenchmarkCRDT(b, "site-a", initial)
		if _, err := c.MergeOps(ops); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDocumentEditingSession measures document-level local edit cost.
func BenchmarkDocumentEditingSession(b *testing.B) {
	geometry := makePolygonJSON(largeRing(500))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc := NewDocument("site-a")
		if err := doc.Apply(InsertFeature{FeatureID: "f", Geometry: geometry}); err != nil {
			b.Fatal(err)
		}
		for step := 0; step < 1000; step++ {
			err := doc.Apply(MoveFeatureVertex{
				FeatureID: "f",
				PartID:    part0,
				RingID:    ring0,
				VertexID:  InitialVertexID(0, step%500),
				Coord:     Coord{X: float64(step), Y: float64(step % 89)},
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func newBenchmarkCRDT(b *testing.B, siteID string, initial json.RawMessage) *GeometryCRDT {
	b.Helper()
	c, err := NewGeometryCRDT(siteID, initial)
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func largeRing(points int) [][][2]float64 {
	ring := make([][2]float64, 0, points+1)
	for i := 0; i < points; i++ {
		ring = append(ring, [2]float64{float64(i), float64((i * i) % 997)})
	}
	ring = append(ring, ring[0])
	return [][][2]float64{ring}
}
