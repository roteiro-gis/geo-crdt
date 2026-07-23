package crdt

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MemStore is an in-memory OpStore and SnapshotStore, suitable for tests and
// as a reference for durable implementations. It is safe for concurrent use.
type MemStore struct {
	mu        sync.Mutex
	ops       []DocumentOp
	seen      map[OpRef]payloadHash
	snapshots map[string]Snapshot
}

// NewMemStore creates an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		seen:      make(map[OpRef]payloadHash),
		snapshots: make(map[string]Snapshot),
	}
}

// Append stores operations, ignoring duplicates by identity.
func (s *MemStore) Append(_ context.Context, ops []DocumentOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hashes := make([]payloadHash, len(ops))
	batch := make(map[OpRef]payloadHash, len(ops))
	for i, op := range ops {
		hash, err := hashDocumentOp(op)
		if err != nil {
			return err
		}
		ref := op.ref()
		if known, ok := s.seen[ref]; ok && known != hash {
			return fmt.Errorf("%w: %s", ErrIdentityCollision, ref)
		}
		if known, ok := batch[ref]; ok && known != hash {
			return fmt.Errorf("%w: %s", ErrIdentityCollision, ref)
		}
		hashes[i] = hash
		batch[ref] = hash
	}
	for i, op := range ops {
		ref := op.ref()
		if _, dup := s.seen[ref]; dup {
			continue
		}
		s.seen[ref] = hashes[i]
		s.ops = append(s.ops, op.normalize())
	}
	return nil
}

// Since returns stored operations beyond the vector clock in (SiteID, Seq)
// order.
func (s *MemStore) Since(_ context.Context, clock VectorClock) ([]DocumentOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []DocumentOp
	for _, op := range s.ops {
		if op.Seq > clock[op.SiteID] {
			result = append(result, op.normalize())
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].SiteID != result[j].SiteID {
			return result[i].SiteID < result[j].SiteID
		}
		return result[i].Seq < result[j].Seq
	})
	return result, nil
}

// Save stores a snapshot under its ID.
func (s *MemStore) Save(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[snapshot.ID] = snapshot
	return nil
}

// Load returns the snapshot stored under an ID.
func (s *MemStore) Load(_ context.Context, id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[id]
	if !ok {
		return Snapshot{}, fmt.Errorf("snapshot %q not found", id)
	}
	return snapshot, nil
}
