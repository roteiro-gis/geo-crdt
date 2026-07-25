package crdt

import "context"

// OpStore is a persistence boundary for document operations. The library
// defines the contract and ships MemStore as a reference implementation;
// applications provide durable backends.
type OpStore interface {
	// Append durably stores operations in one document namespace.
	// Implementations must be idempotent per (DocumentID, SiteID, Seq).
	Append(ctx context.Context, documentID DocumentID, ops []DocumentOp) error

	// Since returns every stored operation for a document beyond the vector
	// clock, in (SiteID, Seq) order.
	Since(ctx context.Context, documentID DocumentID, clock VectorClock) ([]DocumentOp, error)
}

// SnapshotStore is a persistence boundary for document snapshots.
type SnapshotStore interface {
	Save(ctx context.Context, snapshot Snapshot) error
	Load(ctx context.Context, documentID DocumentID, id string) (Snapshot, error)
}
