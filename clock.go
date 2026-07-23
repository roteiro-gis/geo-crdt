package crdt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// MaxTimestamp is the exclusive upper bound for operation timestamps and
// sequence numbers. Remote operations at or above this bound are rejected so
// that a corrupt or hostile peer cannot poison the local clock into
// overflow.
const MaxTimestamp uint64 = 1 << 62

// OpRef identifies one operation by its origin replica and that replica's
// contiguous sequence number (1, 2, 3, ...). Sequence numbers make gaps in
// delivery detectable exactly: a vector clock only advances through
// contiguous prefixes, so missing operations are always re-requested.
type OpRef struct {
	SiteID string `json:"site_id"`
	Seq    uint64 `json:"seq"`
}

// isSet reports whether the reference identifies an operation. The zero
// value refers to base state that predates all operations.
func (r OpRef) isSet() bool {
	return r.Seq != 0 || r.SiteID != ""
}

func (r OpRef) String() string {
	return fmt.Sprintf("%s:%d", r.SiteID, r.Seq)
}

// Stamp is the total-order key used for last-writer-wins resolution.
// Lamport timestamps order first, site IDs break cross-actor ties, and the
// actor sequence breaks ties between operations from the same actor.
type Stamp struct {
	Timestamp uint64 `json:"ts"`
	SiteID    string `json:"site_id"`
	Seq       uint64 `json:"seq"`
}

// isSet reports whether the stamp belongs to an operation. The zero value
// stamps base state and loses to every operation.
func (s Stamp) isSet() bool {
	return s.Timestamp != 0 || s.SiteID != "" || s.Seq != 0
}

// less reports whether s sorts before other in the total operation order.
func (s Stamp) less(other Stamp) bool {
	if s.Timestamp != other.Timestamp {
		return s.Timestamp < other.Timestamp
	}
	if s.SiteID != other.SiteID {
		return s.SiteID < other.SiteID
	}
	return s.Seq < other.Seq
}

// newer reports whether s wins a last-writer-wins comparison against other.
func (s Stamp) newer(other Stamp) bool {
	return other.less(s)
}

// VectorClock maps site IDs to contiguously received sequence numbers: an
// entry of n means operations 1..n from that site are all known.
type VectorClock map[string]uint64

// cloneVectorClock returns an independent copy; the result is never nil.
func cloneVectorClock(clock VectorClock) VectorClock {
	result := make(VectorClock, len(clock))
	for siteID, seq := range clock {
		result[siteID] = seq
	}
	return result
}

// mergeVectorClocks returns the pointwise maximum of a and b.
func mergeVectorClocks(a, b VectorClock) VectorClock {
	result := cloneVectorClock(a)
	for siteID, seq := range b {
		if seq > result[siteID] {
			result[siteID] = seq
		}
	}
	return result
}

// coveredBy reports whether every entry of clock is at or below the
// corresponding entry of other.
func (clock VectorClock) coveredBy(other VectorClock) bool {
	for siteID, seq := range clock {
		if seq > other[siteID] {
			return false
		}
	}
	return true
}

// frontierClock tracks contiguous knowledge per site. Operations received
// beyond a gap are staged; the frontier only advances when the gap fills, so
// vector-clock-driven deltas always re-request missing operations.
type frontierClock struct {
	frontier VectorClock
	staged   map[string]map[uint64]struct{}
}

func newFrontierClock() *frontierClock {
	return &frontierClock{
		frontier: make(VectorClock),
		staged:   make(map[string]map[uint64]struct{}),
	}
}

// observe records receipt of one sequence number, advancing the frontier
// through any staged successors.
func (f *frontierClock) observe(siteID string, seq uint64) {
	if seq <= f.frontier[siteID] {
		return
	}
	if seq == f.frontier[siteID]+1 {
		f.frontier[siteID] = seq
		staged := f.staged[siteID]
		for {
			next := f.frontier[siteID] + 1
			if _, ok := staged[next]; !ok {
				break
			}
			delete(staged, next)
			f.frontier[siteID] = next
		}
		if len(staged) == 0 {
			delete(f.staged, siteID)
		}
		return
	}
	if f.staged[siteID] == nil {
		f.staged[siteID] = make(map[uint64]struct{})
	}
	f.staged[siteID][seq] = struct{}{}
}

// NewSiteID returns a cryptographically random site ID. Site IDs must be
// unique per replica session: reusing a site ID after restoring from a
// snapshot that does not cover all of that site's distributed operations
// mints colliding operation identities. Generating a fresh ID per device
// session is the safe default.
func NewSiteID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand.Read only fails when the platform entropy source is
		// broken; there is no meaningful recovery for ID generation.
		panic(fmt.Sprintf("crdt: reading random site id: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
