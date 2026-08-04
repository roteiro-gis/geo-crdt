package crdt

import (
	"encoding/json"
	"fmt"

	sfgeom "github.com/peterstace/simplefeatures/geom"
)

// ValidatePolygonRings checks that polygon rings (in closed GeoJSON form,
// XY taken from each position) are topologically valid:
//
//   - each ring has at least 4 positions and is closed
//   - no ring is degenerate (zero area relative to its extent)
//   - the exterior ring winds counter-clockwise, interior rings clockwise
//   - complete OGC polygon validity, including ring simplicity, hole
//     containment, ring crossings, and connected interiors
func ValidatePolygonRings(rings [][][]float64) error {
	if len(rings) == 0 {
		return fmt.Errorf("%w: polygon must have at least one ring", ErrInvalidTopology)
	}
	layout := 0
	for i, ring := range rings {
		name := "exterior ring"
		if i > 0 {
			name = fmt.Sprintf("interior ring %d", i)
		}
		if len(ring) < 4 {
			return fmt.Errorf("%w: %s must have at least 4 positions, got %d", ErrInvalidTopology, name, len(ring))
		}
		for j, position := range ring {
			if len(position) != 2 && len(position) != 3 {
				return fmt.Errorf("%w: %s position %d requires exactly 2 or 3 values",
					ErrInvalidTopology, name, j)
			}
			if layout == 0 {
				layout = len(position)
			} else if len(position) != layout {
				return fmt.Errorf("%w: polygon mixes coordinate layouts", ErrInvalidTopology)
			}
			for _, value := range position {
				if !isFinite(value) {
					return fmt.Errorf("%w: %s position %d must be finite", ErrInvalidTopology, name, j)
				}
			}
		}
		first, last := ring[0], ring[len(ring)-1]
		if first[0] != last[0] || first[1] != last[1] {
			return fmt.Errorf("%w: %s is not closed", ErrInvalidTopology, name)
		}

		area := signedAreaXY(ring)
		if i == 0 && area < 0 {
			return fmt.Errorf("%w: exterior ring must be counter-clockwise", ErrInvalidTopology)
		}
		if i > 0 && area > 0 {
			return fmt.Errorf("%w: %s must be clockwise", ErrInvalidTopology, name)
		}
	}
	raw, err := json.Marshal(struct {
		Type        string        `json:"type"`
		Coordinates [][][]float64 `json:"coordinates"`
	}{Type: string(GeometryPolygon), Coordinates: rings})
	if err != nil {
		return fmt.Errorf("%w: encode polygon for validation: %v", ErrInvalidTopology, err)
	}
	if _, err := sfgeom.UnmarshalGeoJSON(raw); err != nil {
		return fmt.Errorf("%w: OGC validity: %v", ErrInvalidTopology, err)
	}
	return nil
}

// RepairPolygonRings applies the selected repairs to polygon rings (closed
// GeoJSON form). Positions keep their full dimension; only XY participates
// in geometric decisions. Dropping a degenerate exterior ring drops the
// whole polygon (an empty result).
func RepairPolygonRings(rings [][][]float64, repairs PolygonRepair) [][][]float64 {
	result := make([][][]float64, 0, len(rings))
	for i, ring := range rings {
		repaired := clonePositions(ring)
		if repairs&RepairRemoveDuplicateVertices != 0 {
			repaired = removeConsecutiveDuplicates(repaired)
		}
		if repairs&RepairCloseRings != 0 {
			repaired = closeRing(repaired)
		}
		if repairs&RepairNormalizeOrientation != 0 && len(repaired) >= 3 {
			area := signedAreaXY(repaired)
			if (i == 0 && area < 0) || (i > 0 && area > 0) {
				reverseRing(repaired)
			}
		}
		if repairs&RepairDropDegenerateRings != 0 && degenerateRing(repaired) {
			if i == 0 {
				return [][][]float64{}
			}
			continue
		}
		result = append(result, repaired)
	}
	return result
}

// --- View helpers used by Document reads and exports ---

// validateGeometryView validates polygon topology of an exported geometry.
// Non-polygon geometries pass.
func validateGeometryView(raw json.RawMessage) error {
	var header geoJSONGeometry
	if err := json.Unmarshal(raw, &header); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
	}
	switch GeometryType(header.Type) {
	case GeometryPolygon:
		var rings [][][]float64
		if err := json.Unmarshal(header.Coordinates, &rings); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		return ValidatePolygonRings(rings)
	case GeometryMultiPolygon:
		var polygons [][][][]float64
		if err := json.Unmarshal(header.Coordinates, &polygons); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		for _, rings := range polygons {
			if err := ValidatePolygonRings(rings); err != nil {
				return err
			}
		}
		if _, err := sfgeom.UnmarshalGeoJSON(raw); err != nil {
			return fmt.Errorf("%w: OGC validity: %v", ErrInvalidTopology, err)
		}
	}
	return nil
}

// repairGeometryView applies polygon repairs to an exported geometry as a
// deterministic view transform. Non-polygon geometries pass through.
func repairGeometryView(raw json.RawMessage, repairs PolygonRepair) json.RawMessage {
	var header geoJSONGeometry
	if err := json.Unmarshal(raw, &header); err != nil {
		return raw
	}
	switch GeometryType(header.Type) {
	case GeometryPolygon:
		var rings [][][]float64
		if err := json.Unmarshal(header.Coordinates, &rings); err != nil {
			return raw
		}
		return marshalView(header.Type, RepairPolygonRings(rings, repairs))
	case GeometryMultiPolygon:
		var polygons [][][][]float64
		if err := json.Unmarshal(header.Coordinates, &polygons); err != nil {
			return raw
		}
		repaired := make([][][][]float64, 0, len(polygons))
		for _, rings := range polygons {
			fixed := RepairPolygonRings(rings, repairs)
			if len(fixed) == 0 && repairs&RepairDropDegenerateRings != 0 {
				continue
			}
			repaired = append(repaired, fixed)
		}
		return marshalView(header.Type, repaired)
	default:
		return raw
	}
}

func marshalView(geometryType string, coordinates any) json.RawMessage {
	data, err := json.Marshal(struct {
		Type        string `json:"type"`
		Coordinates any    `json:"coordinates"`
	}{Type: geometryType, Coordinates: coordinates})
	if err != nil {
		panic(fmt.Sprintf("crdt: marshal repaired geometry: %v", err))
	}
	return data
}

// --- Geometric primitives ---

// signedAreaXY computes the shoelace area over XY; positive is
// counter-clockwise. Works on open and closed rings (the closing duplicate
// contributes a zero term).
func signedAreaXY(ring [][]float64) float64 {
	n := len(ring)
	if n < 3 {
		return 0
	}
	var area float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		area += ring[i][0] * ring[j][1]
		area -= ring[j][0] * ring[i][1]
	}
	return area / 2.0
}

// degenerateRing reports whether a ring has fewer than three distinct
// consecutive positions.
func degenerateRing(ring [][]float64) bool {
	distinct := removeConsecutiveDuplicates(ring)
	if len(distinct) >= 2 && samePositionXY(distinct[0], distinct[len(distinct)-1]) {
		distinct = distinct[:len(distinct)-1]
	}
	return len(distinct) < 3
}

func samePositionXY(a, b []float64) bool {
	return a[0] == b[0] && a[1] == b[1]
}

func clonePositions(ring [][]float64) [][]float64 {
	result := make([][]float64, len(ring))
	for i, position := range ring {
		result[i] = append([]float64(nil), position...)
	}
	return result
}

// removeConsecutiveDuplicates removes consecutive XY-duplicate positions.
func removeConsecutiveDuplicates(ring [][]float64) [][]float64 {
	if len(ring) <= 1 {
		return ring
	}
	result := [][]float64{ring[0]}
	for i := 1; i < len(ring); i++ {
		if !samePositionXY(ring[i], ring[i-1]) {
			result = append(result, ring[i])
		}
	}
	return result
}

// closeRing appends the first position when the ring is open.
func closeRing(ring [][]float64) [][]float64 {
	if len(ring) < 3 {
		return ring
	}
	if !samePositionXY(ring[0], ring[len(ring)-1]) {
		ring = append(ring, append([]float64(nil), ring[0]...))
	}
	return ring
}

// reverseRing reverses ring orientation in place, preserving closure.
func reverseRing(ring [][]float64) {
	closed := len(ring) >= 2 && samePositionXY(ring[0], ring[len(ring)-1])
	n := len(ring)
	if closed {
		n--
	}
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		ring[i], ring[j] = ring[j], ring[i]
	}
	if closed {
		ring[len(ring)-1] = append([]float64(nil), ring[0]...)
	}
}
