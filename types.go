package crdt

import "fmt"

// ID is a stable application-visible identifier used for documents and
// features.
type ID string

// DocumentID is the stable namespace for one collaborative document.
// Operation identities and storage keys are meaningful only within this
// namespace.
type DocumentID string

// NewDocumentID returns a cryptographically random document namespace.
func NewDocumentID() DocumentID {
	return DocumentID(NewSiteID())
}

// Coord is a coordinate value. Coordinates are opaque numeric positions;
// callers own CRS interpretation and transformation. Z is honored only when
// the target geometry carries three dimensions and is otherwise ignored.
type Coord struct {
	X float64
	Y float64
	Z float64
}

// Position returns the coordinate as a GeoJSON position of the given
// dimension count (2 or 3).
func (c Coord) Position(dims int) []float64 {
	if dims >= 3 {
		return []float64{c.X, c.Y, c.Z}
	}
	return []float64{c.X, c.Y}
}

// GeometryType is a GeoJSON geometry type name.
type GeometryType string

const (
	GeometryPoint        GeometryType = "Point"
	GeometryMultiPoint   GeometryType = "MultiPoint"
	GeometryLineString   GeometryType = "LineString"
	GeometryMultiLine    GeometryType = "MultiLineString"
	GeometryPolygon      GeometryType = "Polygon"
	GeometryMultiPolygon GeometryType = "MultiPolygon"
)

// isMulti reports whether the type is a multipart geometry.
func (t GeometryType) isMulti() bool {
	switch t {
	case GeometryMultiPoint, GeometryMultiLine, GeometryMultiPolygon:
		return true
	}
	return false
}

// partType returns the simple geometry type of one part of a multipart
// geometry, or the type itself for simple geometries.
func (t GeometryType) partType() GeometryType {
	switch t {
	case GeometryMultiPoint:
		return GeometryPoint
	case GeometryMultiLine:
		return GeometryLineString
	case GeometryMultiPolygon:
		return GeometryPolygon
	}
	return t
}

// InitialPartID returns the deterministic stable ID assigned to a geometry
// part present in the geometry a feature was created with. Parts added later
// via AddPart receive IDs derived from the creating operation.
func InitialPartID(partIndex int) string {
	return fmt.Sprintf("part:%d", partIndex)
}

// InitialRingID returns the deterministic stable ID assigned to a ring (or
// coordinate sequence) present in the geometry a feature was created with.
// Points and LineStrings use ring index 0; polygon ring 0 is the exterior.
func InitialRingID(partIndex, ringIndex int) string {
	return fmt.Sprintf("ring:%d:%d", partIndex, ringIndex)
}

// InitialVertexID returns the deterministic stable ID assigned to a vertex
// present in the geometry a feature was created with. Positions are indices
// into the open coordinate sequence: polygon rings exclude the closing
// coordinate, which the library re-adds on export.
func InitialVertexID(ringIndex, position int) string {
	return fmt.Sprintf("init:%d:%d", ringIndex, position)
}

// TopologyPolicy controls when the document layer validates polygon topology.
// The CRDT layer always accepts convergent edits; the policy decides whether
// exports of invalid intermediate geometries fail.
type TopologyPolicy int

const (
	// AllowInvalidIntermediate never validates topology; applications
	// validate or repair when they choose to.
	AllowInvalidIntermediate TopologyPolicy = iota

	// ValidateOnExport validates polygon topology when producing snapshots
	// and FeatureCollection exports.
	ValidateOnExport
)

// PolygonRepair is a bitmask of repair actions applied to polygon geometry
// views (feature reads, exports, and snapshots). Repairs are deterministic
// view transforms: they never modify CRDT state.
type PolygonRepair uint8

const (
	RepairNone PolygonRepair = 0

	// RepairCloseRings closes unclosed polygon rings.
	RepairCloseRings PolygonRepair = 1 << iota

	// RepairNormalizeOrientation rewinds exterior rings counter-clockwise
	// and interior rings clockwise.
	RepairNormalizeOrientation

	// RepairRemoveDuplicateVertices removes consecutive duplicate ring
	// vertices.
	RepairRemoveDuplicateVertices

	// RepairDropDegenerateRings removes rings left with fewer than three
	// distinct vertices (for example after concurrent deletes).
	RepairDropDegenerateRings

	// RepairBasicPolygon applies all supported polygon repairs.
	RepairBasicPolygon = RepairCloseRings | RepairNormalizeOrientation |
		RepairRemoveDuplicateVertices | RepairDropDegenerateRings
)

type documentOptions struct {
	topologyPolicy TopologyPolicy
	polygonRepair  PolygonRepair
}

// DocumentOption configures a Document.
type DocumentOption func(*documentOptions)

// WithTopologyPolicy configures when topology validation runs.
func WithTopologyPolicy(policy TopologyPolicy) DocumentOption {
	return func(options *documentOptions) {
		options.topologyPolicy = policy
	}
}

// WithPolygonRepair configures which polygon repairs are applied to geometry
// views produced by feature reads, exports, and snapshots.
func WithPolygonRepair(repair PolygonRepair) DocumentOption {
	return func(options *documentOptions) {
		options.polygonRepair = repair
	}
}
