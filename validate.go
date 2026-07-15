package crdt

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Geometric predicates use tolerances relative to each ring's extent, so
// validation behaves identically for parcel-sized rings in degrees and in
// projected meters. Exact OGC validity (hole containment, ring crossing) is
// out of scope; see the package documentation for the checks performed.
const (
	// relativeAreaEpsilon flags a ring as degenerate when its area is
	// smaller than (extent * relativeAreaEpsilon)^2... i.e. the ring is
	// thinner than a billionth of its own bounding-box diagonal.
	relativeAreaEpsilon = 1e-9

	// relativeCollinearEpsilon bounds the distance (relative to segment
	// length) at which a point counts as lying on a segment.
	relativeCollinearEpsilon = 1e-12
)

// ValidatePolygonRings checks that polygon rings (in closed GeoJSON form,
// XY taken from each position) are topologically valid:
//
//   - each ring has at least 4 positions and is closed
//   - no ring is degenerate (zero area relative to its extent)
//   - the exterior ring winds counter-clockwise, interior rings clockwise
//   - no ring self-intersects
//
// Hole containment and ring-ring crossing are not checked.
func ValidatePolygonRings(rings [][][]float64) error {
	if len(rings) == 0 {
		return fmt.Errorf("%w: polygon must have at least one ring", ErrInvalidTopology)
	}
	for i, ring := range rings {
		name := "exterior ring"
		if i > 0 {
			name = fmt.Sprintf("interior ring %d", i)
		}
		if len(ring) < 4 {
			return fmt.Errorf("%w: %s must have at least 4 positions, got %d", ErrInvalidTopology, name, len(ring))
		}
		first, last := ring[0], ring[len(ring)-1]
		if first[0] != last[0] || first[1] != last[1] {
			return fmt.Errorf("%w: %s is not closed", ErrInvalidTopology, name)
		}

		// Self-intersection is checked before degeneracy: a bowtie has zero
		// signed area, and "self-intersects" is the actionable diagnosis.
		if ringSelfIntersects(ring) {
			return fmt.Errorf("%w: %s self-intersects", ErrInvalidTopology, name)
		}
		area := signedAreaXY(ring)
		if degenerateArea(area, ringExtent(ring)) {
			return fmt.Errorf("%w: %s is degenerate (zero area)", ErrInvalidTopology, name)
		}
		if i == 0 && area < 0 {
			return fmt.Errorf("%w: exterior ring must be counter-clockwise", ErrInvalidTopology)
		}
		if i > 0 && area > 0 {
			return fmt.Errorf("%w: %s must be clockwise", ErrInvalidTopology, name)
		}
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

// ringExtent returns the bounding-box diagonal of a ring, the scale that
// relative tolerances are anchored to.
func ringExtent(ring [][]float64) float64 {
	if len(ring) == 0 {
		return 0
	}
	minX, minY := ring[0][0], ring[0][1]
	maxX, maxY := minX, minY
	for _, position := range ring {
		minX = math.Min(minX, position[0])
		maxX = math.Max(maxX, position[0])
		minY = math.Min(minY, position[1])
		maxY = math.Max(maxY, position[1])
	}
	return math.Hypot(maxX-minX, maxY-minY)
}

func degenerateArea(area, extent float64) bool {
	if extent == 0 {
		return true
	}
	threshold := extent * relativeAreaEpsilon
	return math.Abs(area) <= threshold*threshold
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

// ringSelfIntersects checks non-adjacent edge pairs, pruning with a sweep
// over edge bounding boxes so typical rings cost O(n log n).
func ringSelfIntersects(ring [][]float64) bool {
	n := len(ring) - 1 // closed ring: last edge ends at position 0
	if n < 4 {
		return false
	}

	type edge struct {
		index      int
		minX, maxX float64
		minY, maxY float64
	}
	edges := make([]edge, n)
	for i := 0; i < n; i++ {
		a, b := ring[i], ring[i+1]
		edges[i] = edge{
			index: i,
			minX:  math.Min(a[0], b[0]), maxX: math.Max(a[0], b[0]),
			minY: math.Min(a[1], b[1]), maxY: math.Max(a[1], b[1]),
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].minX < edges[j].minX })

	for i := 0; i < len(edges); i++ {
		for j := i + 1; j < len(edges); j++ {
			if edges[j].minX > edges[i].maxX {
				break
			}
			if edges[j].minY > edges[i].maxY || edges[i].minY > edges[j].maxY {
				continue
			}
			a, b := edges[i].index, edges[j].index
			if a > b {
				a, b = b, a
			}
			// Adjacent edges share a vertex and always "touch".
			if b == a+1 || (a == 0 && b == n-1) {
				continue
			}
			if segmentsIntersectXY(ring[a], ring[a+1], ring[b], ring[b+1]) {
				return true
			}
		}
	}
	return false
}

// segmentsIntersectXY reports whether segments (p1,p2) and (p3,p4) intersect,
// including endpoint touches and collinear overlap, using tolerances
// relative to the segment lengths.
func segmentsIntersectXY(p1, p2, p3, p4 []float64) bool {
	d1 := crossXY(p3, p4, p1)
	d2 := crossXY(p3, p4, p2)
	d3 := crossXY(p1, p2, p3)
	d4 := crossXY(p1, p2, p4)

	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}

	// Collinear/touch cases: |cross| ~ segmentLength * distance, so the
	// threshold scales with the squared segment length.
	len34 := squaredLengthXY(p3, p4)
	len12 := squaredLengthXY(p1, p2)
	if math.Abs(d1) <= relativeCollinearEpsilon*len34 && onSegmentXY(p3, p4, p1) {
		return true
	}
	if math.Abs(d2) <= relativeCollinearEpsilon*len34 && onSegmentXY(p3, p4, p2) {
		return true
	}
	if math.Abs(d3) <= relativeCollinearEpsilon*len12 && onSegmentXY(p1, p2, p3) {
		return true
	}
	if math.Abs(d4) <= relativeCollinearEpsilon*len12 && onSegmentXY(p1, p2, p4) {
		return true
	}
	return false
}

// crossXY computes the cross product of vectors (b-a) and (c-a).
func crossXY(a, b, c []float64) float64 {
	return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
}

func squaredLengthXY(a, b []float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	return dx*dx + dy*dy
}

// onSegmentXY checks whether collinear point p lies within segment (a, b).
func onSegmentXY(a, b, p []float64) bool {
	return math.Min(a[0], b[0]) <= p[0] && p[0] <= math.Max(a[0], b[0]) &&
		math.Min(a[1], b[1]) <= p[1] && p[1] <= math.Max(a[1], b[1])
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
