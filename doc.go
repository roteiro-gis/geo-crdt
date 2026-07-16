// Package crdt provides conflict-free replicated data types for
// collaborative editing of GeoJSON feature collections and geometries.
//
// # Model
//
// A Document is a feature-collection replica. Each feature combines
// independent convergent structures:
//
//   - feature liveness: last-writer-wins between insert_feature and
//     delete_feature (a newer insert resurrects a deleted feature)
//   - properties: one last-writer-wins register per key, with tombstones
//   - geometry identity: a last-writer-wins register over geometry
//     "generations" (each insert_feature/set_geometry defines one); vertex
//     edits are tagged with the generation they target, so edits against a
//     replaced geometry can never corrupt its successor
//   - geometry content: per part and ring, an ordered tree of vertices
//     (an RGA) with stable IDs, monotone delete tombstones, and a
//     last-writer-wins move register per vertex
//
// Because every sub-structure is commutative, idempotent, and associative,
// operations may be delivered in any order, in any batching, any number of
// times: replicas converge byte-identically. Operations whose dependencies
// have not arrived yet (an unseen generation, part, ring, or vertex) are
// buffered and drained automatically; operations that can never apply are
// quarantined and reported in the MergeResult. Merges only return errors
// for protocol violations: malformed envelopes, mismatched base lineages,
// version mismatches, or compaction gaps.
//
// # Identity and ordering
//
// Every operation carries three identity fields: the origin SiteID, a
// contiguous per-site sequence number (Seq), and a Lamport timestamp.
// (SiteID, Seq) identifies the operation; vector clocks advance only
// through contiguous sequence prefixes, so a lost operation is always
// detected and re-requested — sync self-heals. (Timestamp, SiteID) orders
// concurrent writes for last-writer-wins resolution.
//
// Site IDs must be unique per replica session; use NewSiteID. Reusing a
// site ID after restoring from a snapshot that does not cover all of that
// site's distributed operations would mint colliding identities — the
// library detects this and refuses the merge.
//
// # Geometry
//
// All GeoJSON geometry types except GeometryCollection are supported,
// including multipart geometries (with add_part/remove_part) and polygon
// holes (add_ring/remove_ring). Coordinates keep an optional altitude (Z).
// Polygon rings are stored open — the closing coordinate is a property of
// the export — so editing any vertex keeps rings closed by construction.
//
// Concurrent edits can produce topologically invalid intermediate polygons
// (that is inherent to coordinate-level merging, not a bug). The topology
// policy (WithTopologyPolicy) decides whether GeoJSON exports validate, and
// WithPolygonRepair configures deterministic view-level repairs.
// Validation covers ring closure, orientation, degeneracy, and
// self-intersection with tolerances relative to each ring's extent; hole
// containment and ring-ring crossing are not checked.
//
// # Sync
//
// Replicas exchange operations three ways:
//
//   - Delta / MergeDelta: vector-clock-filtered batches carrying lineage
//     and compaction metadata (the recommended transport payload)
//   - PendingOps / MarkSynced: watermark-based outbox for pushing local
//     operations
//   - Snapshot / NewDocumentFromSnapshot: full-fidelity state transfer that
//     compacts history; restored replicas keep syncing with their lineage
//
// Only documents sharing a base lineage (equal BaseHash) can merge:
// documents created empty share one lineage, documents loaded from the
// same FeatureCollection share another, and snapshots inherit the lineage
// they were taken from.
//
// Applications provide transport, persistence (see OpStore/SnapshotStore
// and MemStore), authorization, rendering, and CRS policy. Coordinates are
// opaque numbers to the library.
package crdt
