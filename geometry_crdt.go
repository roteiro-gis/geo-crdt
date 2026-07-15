package crdt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// GeometryCRDT is a standalone convergent replica of a single GeoJSON
// geometry (any type except GeometryCollection). It is a thin wrapper around
// the same engine the Document layer uses, for applications that
// collaborate on one geometry without the feature-collection machinery.
//
// All methods are safe for concurrent use.
type GeometryCRDT struct {
	mu sync.Mutex

	siteID        string
	clock         uint64 // Lamport timestamp
	localSeq      uint64 // contiguous sequence number of local ops
	initHash      string
	state         *geometryState
	ops           []GeometryOp
	seen          map[OpRef]struct{}
	syncedThrough uint64 // local seq watermark covered by MarkSynced
}

// NewGeometryCRDT creates a replica initialized with a shared base GeoJSON
// geometry. Replicas must start from an identical base geometry to merge.
func NewGeometryCRDT(siteID string, initialGeometry json.RawMessage) (*GeometryCRDT, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		siteID = NewSiteID()
	}
	if len(initialGeometry) == 0 {
		return nil, fmt.Errorf("%w: initial geometry is required", ErrInvalidGeometry)
	}
	state, err := newGeometryState(initialGeometry)
	if err != nil {
		return nil, err
	}
	// Hash the canonical re-encoding so formatting differences in the
	// caller's JSON do not split lineages.
	sum := sha256.Sum256(state.geoJSON())
	return &GeometryCRDT{
		siteID:   siteID,
		initHash: hex.EncodeToString(sum[:]),
		state:    state,
		seen:     make(map[OpRef]struct{}),
	}, nil
}

// SiteID returns the local replica site ID.
func (c *GeometryCRDT) SiteID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.siteID
}

// Clock returns the current Lamport clock.
func (c *GeometryCRDT) Clock() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clock
}

// Ops returns a copy of the operation log.
func (c *GeometryCRDT) Ops() []GeometryOp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneGeometryOps(c.ops)
}

// Geometry returns the current geometry as GeoJSON.
func (c *GeometryCRDT) Geometry() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.geoJSON()
}

// Info returns the visible geometry structure with the stable part, ring,
// and vertex IDs used to address edits.
func (c *GeometryCRDT) Info() []PartInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.info()
}

// VertexIDAt resolves the stable ID of the index-th visible vertex of a
// ring.
func (c *GeometryCRDT) VertexIDAt(partID, ringID string, index int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.vertexIDAt(partID, ringID, index)
}

// Apply validates and applies a local operation, assigning it the next
// Lamport timestamp. Use the *Op constructors (InsertVertexOp, AddRingOp,
// ...) to build operations.
func (c *GeometryCRDT) Apply(op GeometryOp) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	nextClock := c.clock + 1
	nextSeq := c.localSeq + 1
	if nextClock >= MaxTimestamp || nextSeq >= MaxTimestamp {
		return fmt.Errorf("%w: clock exhausted", ErrInvalidCommand)
	}
	op.SiteID = c.siteID
	op.Seq = nextSeq
	op.Timestamp = nextClock
	op = op.truncateCoords(c.state.dims).fillDerivedIDs()
	if err := c.state.validateLocalOp(op); err != nil {
		return err
	}

	result, _ := c.state.apply(op)
	if result.outcome != outcomeApplied {
		return fmt.Errorf("%w: local operation did not apply: %s", ErrInvalidCommand, result.reason)
	}
	c.recordOp(op)
	c.clock = nextClock
	c.localSeq = nextSeq
	return nil
}

// Merge merges all operations from a remote replica. Both replicas must
// share the same base geometry.
func (c *GeometryCRDT) Merge(remote *GeometryCRDT) (MergeResult, error) {
	if c == remote {
		return MergeResult{}, nil
	}
	remote.mu.Lock()
	remoteOps := cloneGeometryOps(remote.ops)
	remoteClock := remote.clock
	remoteInitHash := remote.initHash
	remote.mu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if remoteInitHash != c.initHash {
		return MergeResult{}, ErrBaseMismatch
	}
	result, err := c.mergeOpsLocked(remoteOps)
	if err != nil {
		return MergeResult{}, err
	}
	if remoteClock > c.clock {
		c.clock = remoteClock
	}
	return result, nil
}

// MergeOps merges remote wire operations. Operations must carry site IDs
// and timestamps. Merges never fail on operation content: operations whose
// dependencies are missing are buffered until the dependency arrives, and
// permanently inapplicable operations are quarantined in the result.
func (c *GeometryCRDT) MergeOps(ops []GeometryOp) (MergeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mergeOpsLocked(ops)
}

func (c *GeometryCRDT) mergeOpsLocked(incoming []GeometryOp) (MergeResult, error) {
	normalized := make([]GeometryOp, 0, len(incoming))
	nextClock := c.clock
	for _, op := range incoming {
		if err := op.validateEnvelope(); err != nil {
			return MergeResult{}, err
		}
		if op.Timestamp > nextClock {
			nextClock = op.Timestamp
		}
		op.Part = cloneRawMessage(op.Part)
		normalized = append(normalized, op)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Timestamp != normalized[j].Timestamp {
			return normalized[i].Timestamp < normalized[j].Timestamp
		}
		return normalized[i].SiteID < normalized[j].SiteID
	})

	tally := newMergeTally()
	for _, op := range normalized {
		ref := op.ref()
		if _, dup := c.seen[ref]; dup {
			tally.duplicate()
			continue
		}
		result, drained := c.state.apply(op)
		c.recordOp(op)
		tally.record(opOutcome{ref: ref, outcome: result.outcome, reason: result.reason})
		for _, o := range drained {
			tally.record(o)
		}
	}

	if nextClock > c.clock {
		c.clock = nextClock
	}
	return tally.finish(), nil
}

func (c *GeometryCRDT) recordOp(op GeometryOp) {
	c.ops = append(c.ops, op)
	c.seen[op.ref()] = struct{}{}
}

// PendingOps returns local operations not yet marked synced, along with a
// watermark to pass to MarkSynced once the operations are durably sent.
// Operations applied after this call are not covered by the watermark, so
// the PendingOps/MarkSynced pair is race-free.
func (c *GeometryCRDT) PendingOps() ([]GeometryOp, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	watermark := c.syncedThrough
	var pending []GeometryOp
	for _, op := range c.ops {
		if op.SiteID != c.siteID || op.Seq <= c.syncedThrough {
			continue
		}
		pending = append(pending, cloneGeometryOp(op))
		if op.Seq > watermark {
			watermark = op.Seq
		}
	}
	return pending, watermark
}

// MarkSynced records that local operations up to the watermark returned by
// PendingOps have been sent.
func (c *GeometryCRDT) MarkSynced(watermark uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if watermark > c.syncedThrough {
		c.syncedThrough = watermark
	}
}

func cloneGeometryOp(op GeometryOp) GeometryOp {
	op.Part = cloneRawMessage(op.Part)
	if op.Coord != nil {
		op.Coord = append([]float64(nil), op.Coord...)
	}
	if op.Ring != nil {
		ring := make([][]float64, len(op.Ring))
		for i, position := range op.Ring {
			ring[i] = append([]float64(nil), position...)
		}
		op.Ring = ring
	}
	return op
}

func cloneGeometryOps(ops []GeometryOp) []GeometryOp {
	if len(ops) == 0 {
		return nil
	}
	result := make([]GeometryOp, len(ops))
	for i, op := range ops {
		result[i] = cloneGeometryOp(op)
	}
	return result
}
