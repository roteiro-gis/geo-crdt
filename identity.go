package crdt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// payloadHash binds an operation identity to one canonical payload.
type payloadHash [sha256.Size]byte

func hashDocumentOp(op DocumentOp) (payloadHash, error) {
	op = op.normalize().stripEmbeddedIdentity()
	var err error
	if op.Geometry, err = canonicalPayloadJSON(op.Geometry); err != nil {
		return payloadHash{}, fmt.Errorf("%w: canonical geometry: %v", ErrInvalidOp, err)
	}
	for key, value := range op.Properties {
		op.Properties[key], err = canonicalPayloadJSON(value)
		if err != nil {
			return payloadHash{}, fmt.Errorf("%w: canonical property %q: %v", ErrInvalidOp, key, err)
		}
	}
	if op.PropertyValue, err = canonicalPayloadJSON(op.PropertyValue); err != nil {
		return payloadHash{}, fmt.Errorf("%w: canonical property value: %v", ErrInvalidOp, err)
	}
	if op.GeometryOp != nil {
		if op.GeometryOp.Part, err = canonicalPayloadJSON(op.GeometryOp.Part); err != nil {
			return payloadHash{}, fmt.Errorf("%w: canonical part: %v", ErrInvalidOp, err)
		}
	}
	var coordBits []uint64
	var ringBits [][]uint64
	if op.GeometryOp != nil {
		coordBits, ringBits = geometryCoordBits(*op.GeometryOp)
		op.GeometryOp.Coord = nil
		op.GeometryOp.Ring = nil
	}
	return marshalPayloadHash(struct {
		Op        DocumentOp `json:"op"`
		CoordBits []uint64   `json:"coord_bits,omitempty"`
		RingBits  [][]uint64 `json:"ring_bits,omitempty"`
	}{Op: op, CoordBits: coordBits, RingBits: ringBits})
}

func hashGeometryOp(op GeometryOp) (payloadHash, error) {
	var err error
	op.Part, err = canonicalPayloadJSON(op.Part)
	if err != nil {
		return payloadHash{}, fmt.Errorf("%w: canonical part: %v", ErrInvalidOp, err)
	}
	coordBits, ringBits := geometryCoordBits(op)
	op.Coord = nil
	op.Ring = nil
	return marshalPayloadHash(struct {
		Op        GeometryOp `json:"op"`
		CoordBits []uint64   `json:"coord_bits,omitempty"`
		RingBits  [][]uint64 `json:"ring_bits,omitempty"`
	}{Op: op, CoordBits: coordBits, RingBits: ringBits})
}

func geometryCoordBits(op GeometryOp) ([]uint64, [][]uint64) {
	coord := make([]uint64, len(op.Coord))
	for i, value := range op.Coord {
		coord[i] = math.Float64bits(value)
	}
	ring := make([][]uint64, len(op.Ring))
	for i, position := range op.Ring {
		ring[i] = make([]uint64, len(position))
		for j, value := range position {
			ring[i][j] = math.Float64bits(value)
		}
	}
	return coord, ring
}

func marshalPayloadHash(value any) (payloadHash, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return payloadHash{}, fmt.Errorf("%w: canonical payload: %v", ErrInvalidOp, err)
	}
	return sha256.Sum256(data), nil
}

// canonicalPayloadJSON preserves numeric lexemes through json.Number while
// sorting object keys through encoding/json's deterministic map encoding.
func canonicalPayloadJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}
