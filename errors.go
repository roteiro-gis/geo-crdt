package crdt

import "errors"

// Sentinel errors returned by the library. Wrap-aware callers should test with
// errors.Is; most errors carry additional context via fmt.Errorf("%w: ...").
var (
	// ErrInvalidOp reports a malformed operation envelope: missing site ID,
	// zero or out-of-bound timestamp, missing required fields, or invalid
	// payload JSON. Envelope validity is context-free, so every replica
	// classifies the same operation identically.
	ErrInvalidOp = errors.New("crdt: invalid operation")

	// ErrInvalidCommand reports a local command that failed validation
	// against the current document state.
	ErrInvalidCommand = errors.New("crdt: invalid command")

	// ErrUnknownFeature reports a local command that addresses a feature
	// that does not exist or is deleted.
	ErrUnknownFeature = errors.New("crdt: unknown feature")

	// ErrBaseMismatch reports an attempt to merge state from a document with
	// a different base lineage. Replicas can only converge when they share
	// the same origin base (the same initial FeatureCollection, an empty
	// document, or a snapshot descended from one of those).
	ErrBaseMismatch = errors.New("crdt: base lineage mismatch")

	// ErrUnsupportedVersion reports a delta or snapshot with an unknown
	// protocol version.
	ErrUnsupportedVersion = errors.New("crdt: unsupported protocol version")

	// ErrCompactionGap reports that the sender compacted operations this
	// replica has never seen. The receiver must load a full snapshot from
	// the sender instead of merging deltas.
	ErrCompactionGap = errors.New("crdt: compaction gap")

	// ErrUnsupportedGeometry reports a GeoJSON geometry the library does not
	// model (for example GeometryCollection).
	ErrUnsupportedGeometry = errors.New("crdt: unsupported geometry")

	// ErrInvalidGeometry reports GeoJSON that could not be parsed or that
	// violates a structural requirement (coordinate arity, ring size).
	ErrInvalidGeometry = errors.New("crdt: invalid geometry")

	// ErrInvalidTopology reports a topology-policy validation failure during
	// snapshot or export.
	ErrInvalidTopology = errors.New("crdt: invalid topology")
)
