package crdt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// --- Geometry operations ---

// GeometryOpAction enumerates the geometry edit actions.
type GeometryOpAction string

const (
	// ActionInsertVertex inserts a vertex after a stable predecessor.
	ActionInsertVertex GeometryOpAction = "insert_vertex"

	// ActionMoveVertex replaces a vertex coordinate (last-writer-wins).
	ActionMoveVertex GeometryOpAction = "move_vertex"

	// ActionDeleteVertex tombstones a vertex.
	ActionDeleteVertex GeometryOpAction = "delete_vertex"

	// ActionAddRing adds an interior ring (hole) to a polygon part.
	ActionAddRing GeometryOpAction = "add_ring"

	// ActionRemoveRing tombstones an interior ring.
	ActionRemoveRing GeometryOpAction = "remove_ring"

	// ActionAddPart adds a part to a multipart geometry.
	ActionAddPart GeometryOpAction = "add_part"

	// ActionRemovePart tombstones a part of a multipart geometry.
	ActionRemovePart GeometryOpAction = "remove_part"
)

// GeometryOp is one geometry edit. Inside a Document it travels embedded in
// an edit_geometry DocumentOp and inherits the envelope identity; standalone
// GeometryCRDT replicas exchange it with SiteID and Timestamp set. All
// structural references use stable IDs, never indices.
type GeometryOp struct {
	Action GeometryOpAction `json:"action"`

	// SiteID and Seq identify the operation (Seq is the origin site's
	// contiguous sequence number); Timestamp is its Lamport timestamp used
	// for last-writer-wins ordering. All three are omitted on the wire
	// inside document envelopes and inherited from the envelope.
	SiteID    string `json:"site_id,omitempty"`
	Seq       uint64 `json:"seq,omitempty"`
	Timestamp uint64 `json:"ts,omitempty"`

	// PartID addresses a geometry part; RingID a ring or coordinate
	// sequence within it. For add_ring and add_part the created IDs are
	// derived from the operation identity when left empty.
	PartID string `json:"part_id,omitempty"`
	RingID string `json:"ring_id,omitempty"`

	// VertexID is the target of move/delete and the created ID of insert
	// (derived from the operation identity when left empty).
	VertexID string `json:"vertex_id,omitempty"`

	// AfterVertexID is the stable predecessor for inserts; empty inserts at
	// the head of the ring.
	AfterVertexID string `json:"after_vertex_id,omitempty"`

	// Coord is the position for insert_vertex and move_vertex, with 2 or 3
	// values in GeoJSON order.
	Coord []float64 `json:"coord,omitempty"`

	// Ring holds the open coordinate sequence for add_ring.
	Ring [][]float64 `json:"ring,omitempty"`

	// Part holds a simple GeoJSON geometry for add_part.
	Part json.RawMessage `json:"part,omitempty"`
}

func (op GeometryOp) ref() OpRef {
	return OpRef{SiteID: op.SiteID, Seq: op.Seq}
}

func (op GeometryOp) stamp() Stamp {
	return Stamp{Timestamp: op.Timestamp, SiteID: op.SiteID, Seq: op.Seq}
}

// InsertedVertexID returns the stable ID assigned to the vertex created by
// an insert_vertex operation with the given identity. Recorded operations
// carry the ID explicitly; this helper lets applications predict it.
func InsertedVertexID(siteID string, seq uint64) string {
	return fmt.Sprintf("op:%s:%d", siteID, seq)
}

// AddedRingID returns the stable ID of the ring created by an add_ring
// operation with the given identity.
func AddedRingID(siteID string, seq uint64) string {
	return fmt.Sprintf("ring:%s:%d", siteID, seq)
}

// AddedPartID returns the stable ID of the part created by an add_part
// operation with the given identity.
func AddedPartID(siteID string, seq uint64) string {
	return fmt.Sprintf("part:%s:%d", siteID, seq)
}

// vertexID returns the vertex created by an insert, deriving it from the
// operation identity when the wire value is empty.
func (op GeometryOp) vertexID() string {
	if op.VertexID != "" {
		return op.VertexID
	}
	return InsertedVertexID(op.SiteID, op.Seq)
}

// ringID returns the ring created by add_ring.
func (op GeometryOp) ringID() string {
	if op.RingID != "" {
		return op.RingID
	}
	return AddedRingID(op.SiteID, op.Seq)
}

// partID returns the part created by add_part.
func (op GeometryOp) partID() string {
	if op.PartID != "" {
		return op.PartID
	}
	return AddedPartID(op.SiteID, op.Seq)
}

// deriveCreatedIDs makes operation-derived IDs explicit. A supplied ID must
// match the derivation; accepting arbitrary creation IDs would let unrelated
// operations collide and make state depend on arrival order.
func (op GeometryOp) deriveCreatedIDs() (GeometryOp, error) {
	switch op.Action {
	case ActionInsertVertex:
		expected := InsertedVertexID(op.SiteID, op.Seq)
		if op.VertexID != "" && op.VertexID != expected {
			return GeometryOp{}, fmt.Errorf("%w: insert vertex_id %q must equal %q",
				ErrInvalidOp, op.VertexID, expected)
		}
		op.VertexID = expected
	case ActionAddRing:
		expected := AddedRingID(op.SiteID, op.Seq)
		if op.RingID != "" && op.RingID != expected {
			return GeometryOp{}, fmt.Errorf("%w: add ring_id %q must equal %q",
				ErrInvalidOp, op.RingID, expected)
		}
		op.RingID = expected
	case ActionAddPart:
		expected := AddedPartID(op.SiteID, op.Seq)
		if op.PartID != "" && op.PartID != expected {
			return GeometryOp{}, fmt.Errorf("%w: add part_id %q must equal %q",
				ErrInvalidOp, op.PartID, expected)
		}
		op.PartID = expected
	}
	return op, nil
}

// validateEnvelope checks a standalone geometry operation's identity and
// shape. Inside documents the DocumentOp envelope carries identity instead.
func (op GeometryOp) validateEnvelope() error {
	if err := validateIdentity(op.SiteID, op.Seq, op.Timestamp); err != nil {
		return err
	}
	return op.validateShape()
}

// validateIdentity checks the identity triple common to all operations.
func validateIdentity(siteID string, seq, timestamp uint64) error {
	if strings.TrimSpace(siteID) == "" {
		return fmt.Errorf("%w: site_id is required", ErrInvalidOp)
	}
	if seq == 0 || seq >= MaxTimestamp {
		return fmt.Errorf("%w: seq %d out of range", ErrInvalidOp, seq)
	}
	if timestamp == 0 || timestamp >= MaxTimestamp {
		return fmt.Errorf("%w: timestamp %d out of range", ErrInvalidOp, timestamp)
	}
	return nil
}

// validateShape checks the context-free shape of a geometry operation:
// action known and required references present. Coordinate content is
// checked at application time so that a single bad operation quarantines
// rather than failing a whole merge.
func (op GeometryOp) validateShape() error {
	switch op.Action {
	case ActionInsertVertex:
		if op.PartID == "" || op.RingID == "" {
			return fmt.Errorf("%w: insert_vertex requires part_id and ring_id", ErrInvalidOp)
		}
	case ActionMoveVertex, ActionDeleteVertex:
		if op.PartID == "" || op.RingID == "" || op.VertexID == "" {
			return fmt.Errorf("%w: %s requires part_id, ring_id, and vertex_id", ErrInvalidOp, op.Action)
		}
	case ActionAddRing:
		if op.PartID == "" {
			return fmt.Errorf("%w: add_ring requires part_id", ErrInvalidOp)
		}
		if len(op.Ring) == 0 {
			return fmt.Errorf("%w: add_ring requires ring coordinates", ErrInvalidOp)
		}
	case ActionRemoveRing:
		if op.PartID == "" || op.RingID == "" {
			return fmt.Errorf("%w: remove_ring requires part_id and ring_id", ErrInvalidOp)
		}
	case ActionAddPart:
		if len(op.Part) == 0 {
			return fmt.Errorf("%w: add_part requires a part geometry", ErrInvalidOp)
		}
	case ActionRemovePart:
		if op.PartID == "" {
			return fmt.Errorf("%w: remove_part requires part_id", ErrInvalidOp)
		}
	default:
		return fmt.Errorf("%w: unknown geometry action %q", ErrInvalidOp, op.Action)
	}
	return nil
}

// GeometryOp constructors for local application. Identity fields are
// assigned by Apply.

// InsertVertexOp inserts a vertex after a stable predecessor; an empty
// afterVertexID inserts at the head of the ring.
func InsertVertexOp(partID, ringID, afterVertexID string, coord Coord) GeometryOp {
	return GeometryOp{
		Action:        ActionInsertVertex,
		PartID:        partID,
		RingID:        ringID,
		AfterVertexID: afterVertexID,
		Coord:         coord.Position(3),
	}
}

// MoveVertexOp replaces the coordinate of a stable vertex.
func MoveVertexOp(partID, ringID, vertexID string, coord Coord) GeometryOp {
	return GeometryOp{
		Action:   ActionMoveVertex,
		PartID:   partID,
		RingID:   ringID,
		VertexID: vertexID,
		Coord:    coord.Position(3),
	}
}

// DeleteVertexOp tombstones a stable vertex.
func DeleteVertexOp(partID, ringID, vertexID string) GeometryOp {
	return GeometryOp{
		Action:   ActionDeleteVertex,
		PartID:   partID,
		RingID:   ringID,
		VertexID: vertexID,
	}
}

// AddRingOp adds an interior ring to a polygon part. Coordinates form an
// open ring (no closing duplicate); a closed ring is accepted and opened.
func AddRingOp(partID string, coords []Coord) GeometryOp {
	ring := make([][]float64, len(coords))
	for i, coord := range coords {
		ring[i] = coord.Position(3)
	}
	return GeometryOp{Action: ActionAddRing, PartID: partID, Ring: ring}
}

// RemoveRingOp tombstones an interior ring of a polygon part.
func RemoveRingOp(partID, ringID string) GeometryOp {
	return GeometryOp{Action: ActionRemoveRing, PartID: partID, RingID: ringID}
}

// AddPartOp adds a simple geometry as a new part of a multipart geometry.
func AddPartOp(geometry json.RawMessage) GeometryOp {
	return GeometryOp{Action: ActionAddPart, Part: geometry}
}

// RemovePartOp tombstones a part of a multipart geometry.
func RemovePartOp(partID string) GeometryOp {
	return GeometryOp{Action: ActionRemovePart, PartID: partID}
}

// truncateCoords trims local operation coordinates to the geometry's
// dimension count so recorded operations match what the geometry stores.
func (op GeometryOp) truncateCoords(dims int) GeometryOp {
	if len(op.Coord) > dims {
		op.Coord = op.Coord[:dims]
	}
	if op.Ring != nil {
		ring := make([][]float64, len(op.Ring))
		for i, position := range op.Ring {
			if len(position) > dims {
				position = position[:dims]
			}
			ring[i] = position
		}
		op.Ring = ring
	}
	return op
}

// --- Document operations ---

// DocumentOpType enumerates feature, property, and geometry operations.
type DocumentOpType string

const (
	// OpInsertFeature creates (or re-creates) a feature. The newest insert
	// wins both the feature's liveness register and its geometry register.
	OpInsertFeature DocumentOpType = "insert_feature"

	// OpDeleteFeature tombstones a feature (last-writer-wins against
	// inserts, so a newer insert resurrects it).
	OpDeleteFeature DocumentOpType = "delete_feature"

	// OpSetGeometry replaces a feature's geometry, preserving properties.
	OpSetGeometry DocumentOpType = "set_geometry"

	// OpSetProperty sets one property (last-writer-wins per key).
	OpSetProperty DocumentOpType = "set_property"

	// OpDeleteProperty tombstones one property (last-writer-wins per key).
	OpDeleteProperty DocumentOpType = "delete_property"

	// OpEditGeometry applies a GeometryOp to one geometry generation.
	OpEditGeometry DocumentOpType = "edit_geometry"
)

// DocumentOp is the document-level wire operation. Operations are identified
// by (SiteID, Seq) — the origin site's contiguous sequence number — and
// ordered for last-writer-wins resolution by (Timestamp, SiteID, Seq). They carry
// no local bookkeeping and are safe to exchange as deltas in any order:
// merges buffer operations whose dependencies have not arrived.
type DocumentOp struct {
	Type      DocumentOpType `json:"type"`
	SiteID    string         `json:"site_id"`
	Seq       uint64         `json:"seq"`
	Timestamp uint64         `json:"ts"`
	FeatureID ID             `json:"feature_id"`

	// Geometry holds GeoJSON for insert_feature and set_geometry. JSON null
	// (or absence) denotes a feature without geometry.
	Geometry   json.RawMessage            `json:"geometry,omitempty"`
	Properties map[string]json.RawMessage `json:"properties,omitempty"`

	PropertyKey   string          `json:"property_key,omitempty"`
	PropertyValue json.RawMessage `json:"property_value,omitempty"`

	// Gen identifies the geometry generation an edit_geometry op targets:
	// the OpRef of the insert_feature or set_geometry that defined the
	// geometry. Nil targets the base generation of a loaded feature.
	Gen *OpRef `json:"gen,omitempty"`

	// GeometryOp is the edit_geometry payload. Its identity fields are
	// omitted on the wire and inherited from this envelope.
	GeometryOp *GeometryOp `json:"geometry_op,omitempty"`
}

func (op DocumentOp) ref() OpRef {
	return OpRef{SiteID: op.SiteID, Seq: op.Seq}
}

func (op DocumentOp) stamp() Stamp {
	return Stamp{Timestamp: op.Timestamp, SiteID: op.SiteID, Seq: op.Seq}
}

// genRef returns the geometry generation an edit targets; the zero OpRef is
// the base generation.
func (op DocumentOp) genRef() OpRef {
	if op.Gen == nil {
		return OpRef{}
	}
	return *op.Gen
}

// isNullGeometry reports whether a raw geometry value denotes "no geometry".
func isNullGeometry(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// validateEnvelope checks the context-free validity of a wire operation.
// Every replica classifies an operation identically, so envelope failures
// are protocol errors, not merge conflicts.
func (op DocumentOp) validateEnvelope() error {
	if err := validateIdentity(op.SiteID, op.Seq, op.Timestamp); err != nil {
		return err
	}
	if strings.TrimSpace(string(op.FeatureID)) == "" {
		return fmt.Errorf("%w: feature_id is required", ErrInvalidOp)
	}
	switch op.Type {
	case OpInsertFeature:
		if !isNullGeometry(op.Geometry) && !json.Valid(op.Geometry) {
			return fmt.Errorf("%w: insert_feature geometry must be valid JSON", ErrInvalidOp)
		}
		for key, value := range op.Properties {
			if len(value) == 0 || !json.Valid(value) {
				return fmt.Errorf("%w: property %q must be valid JSON", ErrInvalidOp, key)
			}
		}
	case OpDeleteFeature:
	case OpSetGeometry:
		if !isNullGeometry(op.Geometry) && !json.Valid(op.Geometry) {
			return fmt.Errorf("%w: set_geometry geometry must be valid JSON", ErrInvalidOp)
		}
	case OpSetProperty:
		if strings.TrimSpace(op.PropertyKey) == "" {
			return fmt.Errorf("%w: property_key is required for set_property", ErrInvalidOp)
		}
		if len(op.PropertyValue) == 0 || !json.Valid(op.PropertyValue) {
			return fmt.Errorf("%w: property_value must be valid JSON", ErrInvalidOp)
		}
	case OpDeleteProperty:
		if strings.TrimSpace(op.PropertyKey) == "" {
			return fmt.Errorf("%w: property_key is required for delete_property", ErrInvalidOp)
		}
	case OpEditGeometry:
		if op.GeometryOp == nil {
			return fmt.Errorf("%w: geometry_op is required for edit_geometry", ErrInvalidOp)
		}
		if err := op.GeometryOp.validateShape(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown document operation type %q", ErrInvalidOp, op.Type)
	}
	return nil
}

// normalize returns a deep, self-contained copy of a wire operation with the
// geometry payload's identity inherited from the envelope.
func (op DocumentOp) normalize() DocumentOp {
	op.Geometry = cloneRawMessage(op.Geometry)
	op.Properties = cloneProperties(op.Properties)
	op.PropertyValue = cloneRawMessage(op.PropertyValue)
	if op.Gen != nil {
		gen := *op.Gen
		op.Gen = &gen
	}
	if op.GeometryOp != nil {
		geometryOp := *op.GeometryOp
		geometryOp.SiteID = op.SiteID
		geometryOp.Seq = op.Seq
		geometryOp.Timestamp = op.Timestamp
		geometryOp.Part = cloneRawMessage(geometryOp.Part)
		op.GeometryOp = &geometryOp
	}
	return op
}

// stripEmbeddedIdentity clears the geometry payload identity before wire
// encoding; it is redundant with the envelope.
func (op DocumentOp) stripEmbeddedIdentity() DocumentOp {
	if op.GeometryOp != nil {
		geometryOp := *op.GeometryOp
		geometryOp.SiteID = ""
		geometryOp.Seq = 0
		geometryOp.Timestamp = 0
		op.GeometryOp = &geometryOp
	}
	return op
}

// --- Local commands ---

// InsertFeature creates (or re-creates) a feature with a stable ID, optional
// GeoJSON geometry (nil for a feature without geometry), and optional
// JSON-compatible properties. Property writes merge per key with
// last-writer-wins semantics.
type InsertFeature struct {
	FeatureID  ID
	Geometry   json.RawMessage
	Properties map[string]any
}

// DeleteFeature tombstones a feature. A later InsertFeature resurrects it.
type DeleteFeature struct {
	FeatureID ID
}

// SetGeometry replaces a feature's geometry while preserving its properties.
type SetGeometry struct {
	FeatureID ID
	Geometry  json.RawMessage
}

// SetProperty sets or replaces one feature property.
type SetProperty struct {
	FeatureID ID
	Key       string
	Value     any
}

// DeleteProperty tombstones one feature property.
type DeleteProperty struct {
	FeatureID ID
	Key       string
}

// EditGeometry applies a stable-ID geometry operation to a feature.
type EditGeometry struct {
	FeatureID ID
	Op        GeometryOp
}

// MoveFeatureVertex moves a stable vertex of a feature geometry.
type MoveFeatureVertex struct {
	FeatureID ID
	PartID    string
	RingID    string
	VertexID  string
	Coord     Coord
}

// InsertFeatureVertex inserts a vertex after a stable predecessor in a
// feature geometry. An empty AfterVertexID inserts at the head of the ring.
type InsertFeatureVertex struct {
	FeatureID     ID
	PartID        string
	RingID        string
	AfterVertexID string
	Coord         Coord
}

// DeleteFeatureVertex tombstones a stable vertex of a feature geometry.
type DeleteFeatureVertex struct {
	FeatureID ID
	PartID    string
	RingID    string
	VertexID  string
}

// AddFeatureRing adds an interior ring (hole) to a polygon part of a
// feature geometry.
type AddFeatureRing struct {
	FeatureID ID
	PartID    string
	Coords    []Coord
}

// RemoveFeatureRing tombstones an interior ring of a feature geometry.
type RemoveFeatureRing struct {
	FeatureID ID
	PartID    string
	RingID    string
}

// AddFeaturePart adds a simple geometry as a new part of a feature's
// multipart geometry.
type AddFeaturePart struct {
	FeatureID ID
	Geometry  json.RawMessage
}

// RemoveFeaturePart tombstones a part of a feature's multipart geometry.
type RemoveFeaturePart struct {
	FeatureID ID
	PartID    string
}

// geometryOpForCommand converts vertex/ring/part commands to their GeometryOp
// payloads; ok reports whether the command was a geometry edit.
func geometryOpForCommand(command any) (ID, GeometryOp, bool) {
	switch cmd := command.(type) {
	case EditGeometry:
		return cmd.FeatureID, cmd.Op, true
	case MoveFeatureVertex:
		return cmd.FeatureID, MoveVertexOp(cmd.PartID, cmd.RingID, cmd.VertexID, cmd.Coord), true
	case InsertFeatureVertex:
		return cmd.FeatureID, InsertVertexOp(cmd.PartID, cmd.RingID, cmd.AfterVertexID, cmd.Coord), true
	case DeleteFeatureVertex:
		return cmd.FeatureID, DeleteVertexOp(cmd.PartID, cmd.RingID, cmd.VertexID), true
	case AddFeatureRing:
		return cmd.FeatureID, AddRingOp(cmd.PartID, cmd.Coords), true
	case RemoveFeatureRing:
		return cmd.FeatureID, RemoveRingOp(cmd.PartID, cmd.RingID), true
	case AddFeaturePart:
		return cmd.FeatureID, AddPartOp(cmd.Geometry), true
	case RemoveFeaturePart:
		return cmd.FeatureID, RemovePartOp(cmd.PartID), true
	}
	return "", GeometryOp{}, false
}
