package crdt

import (
	"encoding/json"
	"testing"
)

func TestPolygonRequiresThreeDistinctXYPositions(t *testing.T) {
	t.Parallel()

	_, err := NewGeometryCRDT("site-a", json.RawMessage(
		`{"type":"Polygon","coordinates":[[[1,1],[1,1],[1,1],[1,1]]]}`,
	))
	if err == nil {
		t.Fatal("polygon with one repeated position was accepted")
	}
}

func TestGeometryParsingRejectsMixedCoordinateLayouts(t *testing.T) {
	t.Parallel()

	inputs := []json.RawMessage{
		json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[1,1,1]]}`),
		json.RawMessage(`{"type":"Polygon","coordinates":[
			[[0,0],[2,0],[0,2],[0,0]],
			[[0.5,0.5,1],[0.5,1,1],[1,0.5,1],[0.5,0.5,1]]
		]}`),
		json.RawMessage(`{"type":"MultiLineString","coordinates":[
			[[0,0],[1,1]],
			[[2,2,2],[3,3,3]]
		]}`),
		json.RawMessage(`{"type":"Point","coordinates":[1,2,3,4]}`),
	}
	for _, input := range inputs {
		if _, err := NewGeometryCRDT("site-a", input); err == nil {
			t.Fatalf("mixed or unsupported layout was accepted: %s", input)
		}
	}
}

func TestGeometryEditsRejectLayoutMismatchWithoutTruncation(t *testing.T) {
	t.Parallel()

	line, err := NewGeometryCRDT("site-a", json.RawMessage(
		`{"type":"LineString","coordinates":[[0,0],[1,1]]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if line.Layout() != LayoutXY {
		t.Fatalf("layout = %s, want XY", line.Layout())
	}
	err = line.Apply(InsertVertexOp(
		part0, ring0, InitialVertexID(0, 0),
		Coord{X: 0.5, Y: 0.5, Z: 9, Layout: LayoutXYZ},
	))
	if err == nil {
		t.Fatal("XYZ insert into XY geometry was truncated")
	}

	line3D, err := NewGeometryCRDT("site-b", json.RawMessage(
		`{"type":"LineString","coordinates":[[0,0,0],[1,1,1]]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if line3D.Layout() != LayoutXYZ {
		t.Fatalf("layout = %s, want XYZ", line3D.Layout())
	}
	if err := line3D.Apply(MoveVertexOp(
		part0, ring0, InitialVertexID(0, 0),
		Coord{X: 2, Y: 2, Layout: LayoutXY},
	)); err == nil {
		t.Fatal("XY move into XYZ geometry was accepted")
	}
	if err := line3D.Apply(InsertVertexOp(
		part0, ring0, InitialVertexID(0, 0),
		Coord{X: 0.5, Y: 0.5, Z: 0, Layout: LayoutXYZ},
	)); err != nil {
		t.Fatalf("explicit XYZ coordinate with zero Z was rejected: %v", err)
	}
}

func TestAddPartRejectsCoordinateLayoutMismatchLocallyAndRemotely(t *testing.T) {
	t.Parallel()

	initial := json.RawMessage(
		`{"type":"MultiLineString","coordinates":[[[0,0],[1,1]]]}`,
	)
	part3D := json.RawMessage(
		`{"type":"LineString","coordinates":[[2,2,2],[3,3,3]]}`,
	)
	local, err := NewGeometryCRDT("local", initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Apply(AddPartOp(part3D)); err == nil {
		t.Fatal("local XYZ part was accepted by XY multipart geometry")
	}

	remote, err := NewGeometryCRDT("receiver", initial)
	if err != nil {
		t.Fatal(err)
	}
	result, err := remote.MergeOps([]GeometryOp{{
		Action: ActionAddPart, SiteID: "actor", Seq: 1, Timestamp: 1,
		Part: part3D,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("remote layout mismatch was not quarantined: %+v", result)
	}
	var geometry struct {
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(remote.Geometry(), &geometry); err != nil {
		t.Fatal(err)
	}
	if len(geometry.Coordinates) != 1 || len(geometry.Coordinates[0][0]) != 2 {
		t.Fatalf("remote XYZ part changed XY geometry: %v", geometry.Coordinates)
	}
}

func TestDocumentExposesGeometryLayout(t *testing.T) {
	t.Parallel()

	doc := NewDocument("test-document", "site-a")
	mustApply(t, doc, InsertFeature{
		FeatureID: "point",
		Geometry:  json.RawMessage(`{"type":"Point","coordinates":[1,2,0]}`),
	})
	if layout, ok := doc.GeometryLayout("point"); !ok || layout != LayoutXYZ {
		t.Fatalf("document layout = %s, %v", layout, ok)
	}
}
