package crdt

import (
	"encoding/json"
	"testing"
)

func FuzzNewGeometryCRDT(f *testing.F) {
	f.Add([]byte(`{"type":"Point","coordinates":[1,2]}`))
	f.Add([]byte(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`))
	f.Add([]byte(`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`))
	f.Add([]byte(`{"type":"MultiPoint","coordinates":[[0,0],[1,1]]}`))
	f.Add([]byte(`{"type":"MultiPolygon","coordinates":[[[[0,0],[1,0],[1,1],[0,0]]]]}`))
	f.Add([]byte(`{"type":"Point","coordinates":[1,2,3]}`))
	f.Add([]byte(`{"type":"Point","coordinates":[1]}`))
	f.Add([]byte(`not-json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := NewGeometryCRDT("fuzz-site", json.RawMessage(data))
		if err != nil {
			return
		}
		if got := c.Geometry(); !json.Valid(got) {
			t.Fatalf("constructor accepted input but produced invalid JSON: %s", got)
		}
	})
}

func FuzzMergeOps(f *testing.F) {
	f.Add("insert_vertex", "site-b", uint64(1), uint64(1), "part:0", "ring:0:0", "", "init:0:0", 0.5, 0.5)
	f.Add("move_vertex", "site-b", uint64(1), uint64(1), "part:0", "ring:0:0", "init:0:1", "", 2.0, 2.0)
	f.Add("delete_vertex", "site-b", uint64(1), uint64(1), "part:0", "ring:0:0", "init:0:1", "", 0.0, 0.0)
	f.Add("remove_ring", "site-b", uint64(1), uint64(1), "part:0", "ring:0:0", "", "", 0.0, 0.0)
	f.Add("unknown", "", uint64(0), uint64(0), "", "", "", "", 0.0, 0.0)

	f.Fuzz(func(t *testing.T, action, siteID string, seq, ts uint64, partID, ringID, vertexID, afterID string, x, y float64) {
		c, err := NewGeometryCRDT("site-a", makePolygonJSON(squareCoords()))
		if err != nil {
			t.Fatal(err)
		}
		op := GeometryOp{
			Action:        GeometryOpAction(action),
			SiteID:        siteID,
			Seq:           seq % 1024,
			Timestamp:     ts % 1024,
			PartID:        partID,
			RingID:        ringID,
			VertexID:      vertexID,
			AfterVertexID: afterID,
			Coord:         []float64{x, y},
		}
		_, _ = c.MergeOps([]GeometryOp{op})
		if got := c.Geometry(); !json.Valid(got) {
			t.Fatalf("merge produced invalid JSON: %s", got)
		}
	})
}

// FuzzMergeDelta feeds arbitrary JSON through the wire-decoding and merge
// paths of a document.
func FuzzMergeDelta(f *testing.F) {
	seed := NewDocument("test-document", "seed")
	if err := seed.Apply(InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`),
	}); err != nil {
		f.Fatal(err)
	}
	valid, err := json.Marshal(seed.DeltaSince(nil))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"version":2,"site_id":"x","base_hash":"","ops":[]}`))
	f.Add([]byte(`{"version":2,"ops":[{"type":"edit_geometry","site_id":"a","seq":1,"ts":1,"feature_id":"f","geometry_op":{"action":"move_vertex","part_id":"part:0","ring_id":"ring:0:0","vertex_id":"init:0:0","coord":[1,2]}}]}`))
	f.Add([]byte(`not-json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var delta Delta
		if err := json.Unmarshal(data, &delta); err != nil {
			return
		}
		doc := NewDocument("test-document", "fuzz-doc")
		if err := doc.Apply(InsertFeature{
			FeatureID: "f",
			Geometry:  json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`),
		}); err != nil {
			t.Fatal(err)
		}
		_, _ = doc.MergeDelta(delta)
		exported, err := doc.FeatureCollectionJSON()
		if err != nil {
			t.Fatalf("export after fuzz merge: %v", err)
		}
		if !json.Valid(exported) {
			t.Fatalf("export is invalid JSON: %s", exported)
		}
	})
}
