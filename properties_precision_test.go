package crdt

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPropertyNumbersRemainLosslessAcrossReadExportAndSnapshot(t *testing.T) {
	t.Parallel()

	base := json.RawMessage(`{
		"type":"FeatureCollection",
		"features":[{
			"type":"Feature",
			"id":"f",
			"geometry":null,
			"properties":{
				"large":9007199254740993,
				"nested":{"value":18446744073709551615}
			}
		}]
	}`)
	doc, err := NewDocumentFromFeatureCollection("test-document", "site-a", base)
	if err != nil {
		t.Fatal(err)
	}
	assertLosslessPropertyNumbers(t, doc)

	exported, err := doc.FeatureCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(exported, []byte(`9007199254740993`)) ||
		!bytes.Contains(exported, []byte(`18446744073709551615`)) {
		t.Fatalf("GeoJSON export rounded property numbers: %s", exported)
	}

	snapshot, err := doc.Snapshot("checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewDocumentFromSnapshot("site-b", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	assertLosslessPropertyNumbers(t, restored)

	mustApply(t, restored, SetProperty{
		FeatureID: "f",
		Key:       "larger",
		Value:     json.Number("999999999999999999999999999999"),
	})
	feature, _ := restored.Feature("f")
	if got := feature.Properties["larger"]; got != json.Number("999999999999999999999999999999") {
		t.Fatalf("local json.Number changed: %v (%T)", got, got)
	}
}

func TestBaseHashCanonicalizationDoesNotRoundLargeNumbers(t *testing.T) {
	t.Parallel()

	first := json.RawMessage(`{"type":"FeatureCollection","features":[{
		"type":"Feature","id":"f","geometry":null,
		"properties":{"large":9007199254740993,"nested":{"a":1,"b":2}}
	}]}`)
	second := json.RawMessage(`{"features":[{
		"properties":{"nested":{"b":2,"a":1},"large":9007199254740993},
		"geometry":null,"id":"f","type":"Feature"
	}],"type":"FeatureCollection"}`)
	left, err := NewDocumentFromFeatureCollection("test-document", "left", first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewDocumentFromFeatureCollection("test-document", "right", second)
	if err != nil {
		t.Fatal(err)
	}
	if left.BaseHash() != right.BaseHash() {
		t.Fatalf("equivalent lossless JSON split base lineage:\n%s\n%s", left.BaseHash(), right.BaseHash())
	}
}

func assertLosslessPropertyNumbers(t testing.TB, doc *Document) {
	t.Helper()
	feature, ok := doc.Feature("f")
	if !ok {
		t.Fatal("feature not found")
	}
	if got := feature.Properties["large"]; got != json.Number("9007199254740993") {
		t.Fatalf("large number = %v (%T)", got, got)
	}
	nested, ok := feature.Properties["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested property type = %T", feature.Properties["nested"])
	}
	if got := nested["value"]; got != json.Number("18446744073709551615") {
		t.Fatalf("nested number = %v (%T)", got, got)
	}
}
