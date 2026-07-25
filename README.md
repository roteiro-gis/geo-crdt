# geo-crdt

`geo-crdt` is a zero-dependency Go library of conflict-free replicated data
types (CRDTs) for collaborative editing of GeoJSON feature collections —
parcels, trails, utility networks — across replicas that sync over any
transport, in any order, with offline edits.

- **Feature collections**: create/delete/replace features, LWW properties
- **Stable-ID geometry editing**: insert/move/delete vertices, add/remove
  polygon holes and multipart parts — addressed by stable IDs, never indices
- **All GeoJSON geometry types** except GeometryCollection, with optional
  altitude (Z) preserved end to end and `"geometry": null` supported
- **Self-healing sync**: contiguous per-site sequence numbers make delivery
  gaps detectable; vector-clock deltas always re-request missing operations
- **Merges never brick**: operations waiting on missing dependencies buffer
  and drain automatically; permanently inapplicable operations quarantine
  into a `MergeResult` instead of failing the merge
- **Full-fidelity snapshots**: checkpoints carry every register, tombstone,
  and stable ID, so restored replicas keep syncing with their lineage
- **Topology policy**: optional validation on export and deterministic
  polygon repair views (closure, orientation, duplicates, degenerate rings)

Applications provide transport, persistence, authorization, rendering, and
CRS policy. Coordinates are opaque numbers to the library.

## Status

Pre-release. The API and the versioned JSON wire format (protocol version 2)
may change before `v1`.

## Usage

```bash
go test ./...
go run ./examples/geojson-merge
```

```go
doc := crdt.NewDocument("project-123", crdt.NewSiteID())

err := doc.Apply(crdt.InsertFeature{
	FeatureID:  "parcel-123",
	Geometry:   json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`),
	Properties: map[string]any{"owner": "Smith"},
})
if err != nil {
	return err
}

err = doc.Apply(crdt.MoveFeatureVertex{
	FeatureID: "parcel-123",
	PartID:    crdt.InitialPartID(0),
	RingID:    crdt.InitialRingID(0, 0),
	VertexID:  crdt.InitialVertexID(0, 2),
	Coord:     crdt.Coord{X: 11, Y: 11},
})
```

Syncing two replicas:

```go
// Pull-based: ask a peer for everything beyond what we know.
delta := remote.DeltaSince(local.VectorClock())
result, err := local.MergeDelta(delta)

// Push-based: ship the local outbox, acknowledge after the send.
ops, watermark := local.PendingOps()
// ... transmit ops ...
local.MarkSynced(watermark)
```

Checkpointing and compaction:

```go
snapshot, err := doc.Snapshot("nightly")
replica, err := crdt.NewDocumentFromSnapshot(crdt.NewSiteID(), snapshot)
// replica keeps merging deltas from the same lineage.
```

See `examples/` for runnable programs covering merging, sync messages,
parcel editing with holes, snapshots + deltas, and persistence through the
storage interfaces.

## Semantics

| Structure | CRDT | Conflict resolution |
| --- | --- | --- |
| Feature liveness | LWW register pair | Newest insert/delete wins; insert resurrects |
| Geometry identity | LWW register over "generations" | Newest `insert_feature`/`set_geometry` wins; edits are generation-tagged |
| Properties | LWW register per key | Newest write wins; deletes tombstone |
| Vertex order | RGA ordered tree | Concurrent inserts after the same vertex: newest sorts first |
| Vertex position | LWW register per vertex | Newest move wins |
| Vertex/ring/part liveness | Monotone tombstone | Delete wins over concurrent move |

Operation identity is `(site_id, seq)` with contiguous per-site sequence
numbers; LWW ordering is `(ts, site_id)` with Lamport timestamps. Vector
clocks advance only through contiguous prefixes, so lost operations are
always re-requested. Only documents with equal `BaseHash` (same original
base state) can merge.

Concurrent edits can produce topologically invalid intermediate polygons;
that is inherent to coordinate-level merging. Use
`WithTopologyPolicy(crdt.ValidateOnExport)` to gate exports and
`WithPolygonRepair` for deterministic view-level repairs. Validation covers
closure, orientation, degeneracy, and self-intersection with tolerances
relative to each ring's extent; hole containment is not checked.

## License

MIT OR Apache-2.0.
