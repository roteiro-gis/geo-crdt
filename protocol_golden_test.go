package crdt

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProtocolV3GoldenVectors(t *testing.T) {
	source := goldenProtocolDocument(t)
	delta := source.DeltaSince(nil)
	snapshot, err := source.Snapshot("protocol-v3")
	if err != nil {
		t.Fatal(err)
	}

	assertGoldenJSON(t, "delta.json", delta)
	assertGoldenJSON(t, "snapshot.json", snapshot)

	deltaWire := readGolden(t, "delta.json")
	var decodedDelta Delta
	if err := json.Unmarshal(deltaWire, &decodedDelta); err != nil {
		t.Fatalf("decode golden delta: %v", err)
	}
	receiver := NewDocument("protocol-golden", "actor-b")
	if _, err := receiver.MergeDelta(decodedDelta); err != nil {
		t.Fatalf("merge golden delta: %v", err)
	}
	if got, want := documentJSON(t, receiver), documentJSON(t, source); got != want {
		t.Fatalf("golden delta state mismatch\ngot:  %s\nwant: %s", got, want)
	}

	snapshotWire := readGolden(t, "snapshot.json")
	var decodedSnapshot Snapshot
	if err := json.Unmarshal(snapshotWire, &decodedSnapshot); err != nil {
		t.Fatalf("decode golden snapshot: %v", err)
	}
	restored, err := NewDocumentFromSnapshot("actor-b", decodedSnapshot)
	if err != nil {
		t.Fatalf("restore golden snapshot: %v", err)
	}
	if got, want := documentJSON(t, restored), documentJSON(t, source); got != want {
		t.Fatalf("golden snapshot state mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func goldenProtocolDocument(t *testing.T) *Document {
	t.Helper()
	doc := NewDocument("protocol-golden", "actor-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "parcel",
		Geometry: json.RawMessage(
			`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`),
		Properties: map[string]any{
			"large": json.Number("9007199254740993"),
			"owner": "Ada",
		},
	})
	mustApply(t, doc, MoveFeatureVertex{
		FeatureID: "parcel",
		PartID:    InitialPartID(0),
		RingID:    InitialRingID(0, 0),
		VertexID:  InitialVertexID(0, 2),
		Coord:     Coord{X: 11, Y: 11},
	})
	mustApply(t, doc, AddFeatureRing{
		FeatureID: "parcel",
		PartID:    InitialPartID(0),
		Coords: []Coord{
			{X: 2, Y: 2},
			{X: 2, Y: 4},
			{X: 4, Y: 4},
			{X: 4, Y: 2},
		},
	})
	mustApply(t, doc, SetProperty{FeatureID: "parcel", Key: "status", Value: "review"})
	mustApply(t, doc, DeleteProperty{FeatureID: "parcel", Key: "owner"})
	mustApply(t, doc, InsertFeature{
		FeatureID: "retired",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[-71,42,8]}`),
		Properties: map[string]any{
			"active": false,
		},
	})
	mustApply(t, doc, DeleteFeature{FeatureID: "retired"})
	return doc
}

func assertGoldenJSON(t *testing.T, name string, value any) {
	t.Helper()
	wire, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wire = append(wire, '\n')
	path := filepath.Join("testdata", "protocol", "v3", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, wire, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected := readGolden(t, name)
	if !bytes.Equal(wire, expected) {
		t.Fatalf("%s changed; inspect the protocol change and run UPDATE_GOLDEN=1 go test -run TestProtocolV3GoldenVectors .", path)
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "protocol", "v3", name)
	wire, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}
