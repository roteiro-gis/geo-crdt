package crdt

import (
	"encoding/json"
	"strings"
	"testing"
)

func closedSquare(scale float64) [][]float64 {
	return [][]float64{
		{0, 0}, {scale, 0}, {scale, scale}, {0, scale}, {0, 0},
	}
}

func TestValidatePolygonRings(t *testing.T) {
	tests := []struct {
		name    string
		rings   [][][]float64
		wantErr string
	}{
		{"valid square", [][][]float64{closedSquare(10)}, ""},
		{"no rings", nil, "at least one ring"},
		{"too few positions", [][][]float64{{{0, 0}, {1, 1}, {0, 0}}}, "at least 4"},
		{"unclosed", [][][]float64{{{0, 0}, {10, 0}, {10, 10}, {0, 10}}}, "not closed"},
		{"cw exterior", [][][]float64{{{0, 0}, {0, 10}, {10, 10}, {10, 0}, {0, 0}}}, "counter-clockwise"},
		{"zero area", [][][]float64{{{0, 0}, {5, 0}, {10, 0}, {0, 0}}}, "degenerate"},
		{"bowtie", [][][]float64{{{0, 0}, {10, 10}, {10, 0}, {0, 10}, {0, 0}}}, "self-intersects"},
		{
			"valid hole",
			[][][]float64{
				{{0, 0}, {20, 0}, {20, 20}, {0, 20}, {0, 0}},
				{{5, 5}, {5, 15}, {15, 15}, {15, 5}, {5, 5}},
			},
			"",
		},
		{
			"ccw hole",
			[][][]float64{
				{{0, 0}, {20, 0}, {20, 20}, {0, 20}, {0, 0}},
				{{5, 5}, {15, 5}, {15, 15}, {5, 15}, {5, 5}},
			},
			"clockwise",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePolygonRings(tt.rings)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// Regression (review finding A8): tolerances scale with the geometry, so a
// tiny-but-real parcel in degrees and a huge ring in projected meters both
// validate.
func TestValidatePolygonRings_ScaleIndependence(t *testing.T) {
	// ~1 m² triangle expressed in degrees (~1e-5 degrees per meter).
	small := [][][]float64{{
		{-122.40000, 37.70000},
		{-122.39999, 37.70000},
		{-122.399995, 37.70001},
		{-122.40000, 37.70000},
	}}
	if err := ValidatePolygonRings(small); err != nil {
		t.Fatalf("small degree-scale parcel flagged invalid: %v", err)
	}

	// The same shape in UTM-like meters at large offsets.
	large := [][][]float64{{
		{500000, 4000000},
		{500100, 4000000},
		{500050, 4000100},
		{500000, 4000000},
	}}
	if err := ValidatePolygonRings(large); err != nil {
		t.Fatalf("projected-meter parcel flagged invalid: %v", err)
	}
}

// Regression (review finding A3): repairs honor the bitmask exactly.
func TestRepairPolygonRings_HonorsBitmask(t *testing.T) {
	// Clockwise, unclosed, with a duplicate vertex.
	ring := [][]float64{{0, 0}, {0, 10}, {0, 10}, {10, 10}, {10, 0}}

	closed := RepairPolygonRings([][][]float64{ring}, RepairCloseRings)
	got := closed[0]
	if !samePositionXY(got[0], got[len(got)-1]) {
		t.Fatal("RepairCloseRings should close the ring")
	}
	if len(got) != 6 {
		t.Fatalf("RepairCloseRings must not deduplicate: %v", got)
	}
	if signedAreaXY(got) > 0 {
		t.Fatal("RepairCloseRings must not change orientation")
	}

	dedup := RepairPolygonRings([][][]float64{ring}, RepairRemoveDuplicateVertices)
	if len(dedup[0]) != 4 {
		t.Fatalf("RepairRemoveDuplicateVertices: %v", dedup[0])
	}
	if samePositionXY(dedup[0][0], dedup[0][len(dedup[0])-1]) {
		t.Fatal("RepairRemoveDuplicateVertices must not close the ring")
	}

	oriented := RepairPolygonRings([][][]float64{ring}, RepairNormalizeOrientation)
	if signedAreaXY(oriented[0]) < 0 {
		t.Fatal("RepairNormalizeOrientation should rewind the exterior CCW")
	}

	full := RepairPolygonRings([][][]float64{ring}, RepairBasicPolygon)
	if err := ValidatePolygonRings(full); err != nil {
		t.Fatalf("fully repaired ring should validate: %v", err)
	}
}

func TestRepairPolygonRings_DropsDegenerateRings(t *testing.T) {
	rings := [][][]float64{
		closedSquare(20),
		{{5, 5}, {6, 6}, {5, 5}}, // degenerate hole
	}
	repaired := RepairPolygonRings(rings, RepairDropDegenerateRings)
	if len(repaired) != 1 {
		t.Fatalf("expected degenerate hole dropped, got %d rings", len(repaired))
	}

	// A degenerate exterior drops the whole polygon.
	repaired = RepairPolygonRings([][][]float64{{{0, 0}, {1, 1}, {0, 0}}}, RepairDropDegenerateRings)
	if len(repaired) != 0 {
		t.Fatalf("expected empty polygon, got %v", repaired)
	}
}

func TestRepairPolygonRings_PreservesZ(t *testing.T) {
	ring := [][]float64{{0, 0, 5}, {0, 10, 6}, {10, 10, 7}, {10, 0, 8}}
	repaired := RepairPolygonRings([][][]float64{ring}, RepairBasicPolygon)
	for _, position := range repaired[0] {
		if len(position) != 3 {
			t.Fatalf("altitude lost during repair: %v", repaired[0])
		}
	}
}

func TestDocumentExportTopologyPolicy(t *testing.T) {
	// A concurrent-merge shape that self-intersects exports fine by default
	// but fails under ValidateOnExport.
	bowtie := json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,10],[10,0],[0,10],[0,0]]]}`)

	lenient := NewDocument("test-document", "site-a")
	mustApply(t, lenient, InsertFeature{FeatureID: "f", Geometry: bowtie})
	if _, err := lenient.FeatureCollectionJSON(); err != nil {
		t.Fatalf("lenient export should pass: %v", err)
	}

	strict := NewDocument("test-document", "site-a", WithTopologyPolicy(ValidateOnExport))
	mustApply(t, strict, InsertFeature{FeatureID: "f", Geometry: bowtie})
	if _, err := strict.FeatureCollectionJSON(); err == nil {
		t.Fatal("ValidateOnExport should reject a self-intersecting polygon")
	}
}

func TestDocumentPolygonRepairView(t *testing.T) {
	doc := NewDocument("test-document", "site-a", WithPolygonRepair(RepairBasicPolygon))
	// Clockwise exterior; the view repairs orientation without touching
	// CRDT state.
	mustApply(t, doc, InsertFeature{
		FeatureID: "f",
		Geometry:  json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[0,10],[10,10],[10,0],[0,0]]]}`),
	})
	feature, _ := doc.Feature("f")
	var geom struct {
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Geometry, &geom); err != nil {
		t.Fatal(err)
	}
	if signedAreaXY(geom.Coordinates[0]) < 0 {
		t.Fatalf("repair view should normalize orientation: %v", geom.Coordinates[0])
	}
}
