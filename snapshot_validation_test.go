package crdt

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSnapshotRestoreRejectsMalformedStateWithoutPanicking(t *testing.T) {
	t.Parallel()

	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "point",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[1,2]}`),
	})
	valid, err := doc.Snapshot("checkpoint")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"overflow clock", func(snapshot *Snapshot) {
			snapshot.Clock = ^uint64(0)
		}},
		{"missing outbox operation", func(snapshot *Snapshot) {
			snapshot.OutboxOps = nil
		}},
		{"duplicate feature", func(snapshot *Snapshot) {
			snapshot.Features = append(snapshot.Features, snapshot.Features[0])
		}},
		{"invalid property JSON", func(snapshot *Snapshot) {
			snapshot.Features[0].Properties = map[string]PropertySnapshot{
				"bad": {Value: json.RawMessage(`{`)},
			}
		}},
		{"generation stamp mismatch", func(snapshot *Snapshot) {
			snapshot.Features[0].GenStamp.Seq++
		}},
		{"point without parts", func(snapshot *Snapshot) {
			snapshot.Features[0].Geometry.Parts = nil
		}},
		{"point without rings", func(snapshot *Snapshot) {
			snapshot.Features[0].Geometry.Parts[0].Rings = nil
		}},
		{"point without vertices", func(snapshot *Snapshot) {
			snapshot.Features[0].Geometry.Parts[0].Rings[0].Vertices = nil
		}},
		{"coordinate arity mismatch", func(snapshot *Snapshot) {
			snapshot.Features[0].Geometry.Parts[0].Rings[0].Vertices[0].Coord = []float64{1}
		}},
		{"missing ordering key", func(snapshot *Snapshot) {
			snapshot.Features[0].Geometry.Parts[0].Key = KeySnapshot{}
		}},
		{"retained operation below frontier", func(snapshot *Snapshot) {
			snapshot.RetainedOps = cloneDocumentOps(snapshot.OutboxOps)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneSnapshot(valid)
			test.mutate(&snapshot)
			assertNoPanic(t, func() {
				restored, err := NewDocumentFromSnapshot("site-b", snapshot)
				if !errors.Is(err, ErrInvalidSnapshot) {
					t.Fatalf("restore error = %v", err)
				}
				if restored != nil {
					t.Fatal("malformed snapshot returned a document")
				}
			})
		})
	}
}

func TestSnapshotRestoreRejectsNoncanonicalGeometryTrees(t *testing.T) {
	t.Parallel()

	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "line",
		Geometry:  json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1]]}`),
	})
	snapshot, err := doc.Snapshot("checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	vertices := snapshot.Features[0].Geometry.Parts[0].Rings[0].Vertices
	vertices[1].Parent = ""
	if _, err := NewDocumentFromSnapshot("site-b", snapshot); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("noncanonical tree error = %v", err)
	}
}

func FuzzRestoreSnapshotDoesNotPanic(f *testing.F) {
	doc := NewDocument("test-document", "site-a")
	if err := doc.Apply(InsertFeature{
		FeatureID: "point",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[1,2]}`),
	}); err != nil {
		f.Fatal(err)
	}
	snapshot, err := doc.Snapshot("seed")
	if err != nil {
		f.Fatal(err)
	}
	wire, err := json.Marshal(snapshot)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(wire)
	f.Add([]byte(`{"version":3,"document_id":"d","site_id":"s","base_hash":"x","features":[{"id":"f","geometry":{"type":"Point","dims":2}}]}`))
	f.Add([]byte(`not-json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var candidate Snapshot
		if err := json.Unmarshal(data, &candidate); err != nil {
			return
		}
		restored, err := NewDocumentFromSnapshot("fuzz-site", candidate)
		if err != nil {
			return
		}
		_, _ = restored.FeatureCollectionJSON()
	})
}

func assertNoPanic(t testing.TB, fn func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("panicked: %v", recovered)
		}
	}()
	fn()
}
