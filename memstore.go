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
	documents map[DocumentID]*memDocument
	snapshots map[DocumentID]map[string]Snapshot
}

type memDocument struct {
	ops  []DocumentOp
	seen map[OpRef]payloadHash
}

// NewMemStore creates an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		documents: make(map[DocumentID]*memDocument),
		snapshots: make(map[DocumentID]map[string]Snapshot),
	}
}

func (s *MemStore) document(documentID DocumentID) (*memDocument, error) {
	if documentID == "" {
		return nil, fmt.Errorf("document_id is required")
	}
	document := s.documents[documentID]
	if document == nil {
		document = &memDocument{seen: make(map[OpRef]payloadHash)}
		s.documents[documentID] = document
	}
	return document, nil
}

// Append stores operations in one document, ignoring exact duplicates.
func (s *MemStore) Append(_ context.Context, documentID DocumentID, ops []DocumentOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.document(documentID)
	if err != nil {
		return err
	}
	hashes := make([]payloadHash, len(ops))
	batch := make(map[OpRef]payloadHash, len(ops))
	for i, op := range ops {
		hash, err := hashDocumentOp(op)
		if err != nil {
			return err
		}
		ref := op.ref()
		if known, ok := document.seen[ref]; ok && known != hash {
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
		if _, dup := document.seen[ref]; dup {
			continue
		}
		document.seen[ref] = hashes[i]
		document.ops = append(document.ops, op.normalize())
	}
	return nil
}

// Since returns one document's stored operations beyond the vector clock.
func (s *MemStore) Since(_ context.Context, documentID DocumentID, clock VectorClock) ([]DocumentOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.document(documentID)
	if err != nil {
		return nil, err
	}

	var result []DocumentOp
	for _, op := range document.ops {
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
	if snapshot.DocumentID == "" {
		return fmt.Errorf("document_id is required")
	}
	if s.snapshots[snapshot.DocumentID] == nil {
		s.snapshots[snapshot.DocumentID] = make(map[string]Snapshot)
	}
	s.snapshots[snapshot.DocumentID][snapshot.ID] = snapshot
	return nil
}

// Load returns the snapshot stored under a document-scoped ID.
func (s *MemStore) Load(_ context.Context, documentID DocumentID, id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[documentID][id]
	if !ok {
		return Snapshot{}, fmt.Errorf("snapshot %q for document %q not found", id, documentID)
	}
	return snapshot, nil
}
