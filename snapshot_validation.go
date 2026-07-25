package crdt

import (
	"encoding/json"
	"fmt"
	"strings"
)

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.BaseHash == "" {
		return invalidSnapshot("missing base_hash")
	}
	if strings.TrimSpace(string(snapshot.DocumentID)) == "" {
		return invalidSnapshot("missing document_id")
	}
	if strings.TrimSpace(snapshot.SiteID) == "" {
		return invalidSnapshot("missing site_id")
	}
	if snapshot.Clock >= MaxTimestamp {
		return invalidSnapshot("clock %d out of range", snapshot.Clock)
	}
	if err := validateWireVectorClock("vector_clock", snapshot.VectorClock); err != nil {
		return invalidSnapshot("%v", err)
	}
	if err := validateWireVectorClock("applied", snapshot.Applied); err != nil {
		return invalidSnapshot("%v", err)
	}
	if !snapshot.VectorClock.coveredBy(snapshot.Applied) {
		return invalidSnapshot("vector_clock exceeds applied knowledge")
	}
	if snapshot.SyncedThrough > snapshot.Applied[snapshot.SiteID] {
		return invalidSnapshot("synced_through %d exceeds applied sequence %d for %q",
			snapshot.SyncedThrough, snapshot.Applied[snapshot.SiteID], snapshot.SiteID)
	}

	featureIDs := make(map[ID]struct{}, len(snapshot.Features))
	for i, feature := range snapshot.Features {
		if strings.TrimSpace(string(feature.ID)) == "" {
			return invalidSnapshot("feature %d has an empty id", i)
		}
		if _, duplicate := featureIDs[feature.ID]; duplicate {
			return invalidSnapshot("duplicate feature id %q", feature.ID)
		}
		featureIDs[feature.ID] = struct{}{}
		if err := validateFeatureSnapshot(feature, snapshot.Clock, snapshot.Applied); err != nil {
			return fmt.Errorf("%w: feature %q: %v", ErrInvalidSnapshot, feature.ID, err)
		}
	}
	return validateSnapshotOperationSets(snapshot)
}

func validateFeatureSnapshot(feature FeatureSnapshot, clock uint64, applied VectorClock) error {
	if err := validateOptionalSnapshotStamp("create register", feature.CreateReg, clock, applied); err != nil {
		return err
	}
	if err := validateOptionalSnapshotStamp("delete register", feature.DeleteReg, clock, applied); err != nil {
		return err
	}
	if feature.GenID == nil && feature.GenStamp != nil {
		return fmt.Errorf("generation stamp has no generation id")
	}
	if feature.GenID != nil {
		if err := validateSnapshotRef("generation id", *feature.GenID, applied); err != nil {
			return err
		}
		if feature.GenStamp == nil {
			return fmt.Errorf("generation %s has no stamp", feature.GenID)
		}
		if err := validateSnapshotStamp("generation stamp", *feature.GenStamp, clock, applied); err != nil {
			return err
		}
		if feature.GenStamp.SiteID != feature.GenID.SiteID || feature.GenStamp.Seq != feature.GenID.Seq {
			return fmt.Errorf("generation id %s does not match its stamp", feature.GenID)
		}
	}
	seenGenerations := make(map[OpRef]struct{}, len(feature.SeenGens))
	for _, ref := range feature.SeenGens {
		if err := validateSnapshotRef("seen generation", ref, applied); err != nil {
			return err
		}
		if _, duplicate := seenGenerations[ref]; duplicate {
			return fmt.Errorf("duplicate seen generation %s", ref)
		}
		seenGenerations[ref] = struct{}{}
	}
	if feature.GenID != nil {
		if _, ok := seenGenerations[*feature.GenID]; !ok {
			return fmt.Errorf("current generation %s is absent from seen generations", feature.GenID)
		}
	}
	for key, property := range feature.Properties {
		if property.Reg != nil {
			if err := validateSnapshotStamp("property "+key, *property.Reg, clock, applied); err != nil {
				return err
			}
		}
		if len(property.Value) == 0 {
			if !property.Deleted {
				return fmt.Errorf("property %q has no value", key)
			}
		} else if !json.Valid(property.Value) {
			return fmt.Errorf("property %q contains invalid JSON", key)
		}
	}
	if feature.Geometry != nil {
		if !feature.Base && feature.GenID == nil {
			return fmt.Errorf("geometry has neither a base nor operation generation")
		}
		if err := validateGeometrySnapshot(feature.Geometry, clock, applied); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotOperationSets(snapshot Snapshot) error {
	allHashes := make(map[OpRef]payloadHash)
	retainedHashes := make(map[OpRef]payloadHash)
	outboxRefs := make(map[OpRef]struct{})

	validateGroup := func(name string, ops []DocumentOp) error {
		for _, op := range ops {
			normalized, err := normalizeSnapshotOp(op)
			if err != nil {
				return fmt.Errorf("%s op: %w", name, err)
			}
			if normalized.Timestamp > snapshot.Clock {
				return fmt.Errorf("%s op %s timestamp %d exceeds snapshot clock %d",
					name, normalized.ref(), normalized.Timestamp, snapshot.Clock)
			}
			if normalized.Seq > snapshot.Applied[normalized.SiteID] {
				return fmt.Errorf("%s op %s exceeds applied knowledge", name, normalized.ref())
			}
			hash, err := hashDocumentOp(normalized)
			if err != nil {
				return err
			}
			if known, ok := allHashes[normalized.ref()]; ok && known != hash {
				return fmt.Errorf("%w: %s", ErrIdentityCollision, normalized.ref())
			}
			allHashes[normalized.ref()] = hash
		}
		return nil
	}

	if err := validateGroup("retained", snapshot.RetainedOps); err != nil {
		return invalidSnapshot("%v", err)
	}
	for _, op := range snapshot.RetainedOps {
		normalized, _ := normalizeSnapshotOp(op)
		if normalized.Seq <= snapshot.VectorClock[normalized.SiteID] {
			return invalidSnapshot("retained op %s is not beyond the contiguous frontier", normalized.ref())
		}
		hash, _ := hashDocumentOp(normalized)
		if _, duplicate := retainedHashes[normalized.ref()]; duplicate {
			return invalidSnapshot("duplicate retained op %s", normalized.ref())
		}
		retainedHashes[normalized.ref()] = hash
	}

	if err := validateGroup("outbox", snapshot.OutboxOps); err != nil {
		return invalidSnapshot("%v", err)
	}
	for _, op := range snapshot.OutboxOps {
		normalized, _ := normalizeSnapshotOp(op)
		if normalized.SiteID != snapshot.SiteID {
			return invalidSnapshot("outbox op %s belongs to another site", normalized.ref())
		}
		if normalized.Seq <= snapshot.SyncedThrough {
			return invalidSnapshot("outbox op %s is already acknowledged", normalized.ref())
		}
		if _, duplicate := outboxRefs[normalized.ref()]; duplicate {
			return invalidSnapshot("duplicate outbox op %s", normalized.ref())
		}
		outboxRefs[normalized.ref()] = struct{}{}
	}
	expectedOutbox := snapshot.Applied[snapshot.SiteID] - snapshot.SyncedThrough
	if uint64(len(outboxRefs)) != expectedOutbox {
		return invalidSnapshot("outbox contains %d operations, want %d", len(outboxRefs), expectedOutbox)
	}

	if err := validateGroup("pending", snapshot.PendingOps); err != nil {
		return invalidSnapshot("%v", err)
	}
	pendingRefs := make(map[OpRef]struct{}, len(snapshot.PendingOps))
	for _, op := range snapshot.PendingOps {
		normalized, _ := normalizeSnapshotOp(op)
		if normalized.Type != OpEditGeometry {
			return invalidSnapshot("pending op %s is not a geometry edit", normalized.ref())
		}
		if _, duplicate := pendingRefs[normalized.ref()]; duplicate {
			return invalidSnapshot("duplicate pending op %s", normalized.ref())
		}
		pendingRefs[normalized.ref()] = struct{}{}
		if normalized.Seq > snapshot.VectorClock[normalized.SiteID] {
			hash, retained := retainedHashes[normalized.ref()]
			if !retained || hash != allHashes[normalized.ref()] {
				return invalidSnapshot("sparse pending op %s is absent from retained history", normalized.ref())
			}
		}
	}
	return nil
}

func validateGeometrySnapshot(snapshot *GeometrySnapshot, clock uint64, applied VectorClock) error {
	if snapshot.Dims != 2 && snapshot.Dims != 3 {
		return fmt.Errorf("geometry dims %d are invalid", snapshot.Dims)
	}
	switch snapshot.Type {
	case GeometryPoint, GeometryLineString, GeometryPolygon,
		GeometryMultiPoint, GeometryMultiLine, GeometryMultiPolygon:
	default:
		return fmt.Errorf("unsupported geometry type %q", snapshot.Type)
	}
	if !snapshot.Type.isMulti() && len(snapshot.Parts) != 1 {
		return fmt.Errorf("%s requires exactly one part, got %d", snapshot.Type, len(snapshot.Parts))
	}

	partIDs := make(map[string]struct{}, len(snapshot.Parts))
	var previousPartKey seqKey
	for partIndex, part := range snapshot.Parts {
		if strings.TrimSpace(part.ID) == "" {
			return fmt.Errorf("part %d has an empty id", partIndex)
		}
		if _, duplicate := partIDs[part.ID]; duplicate {
			return fmt.Errorf("duplicate part id %q", part.ID)
		}
		partIDs[part.ID] = struct{}{}
		key, err := validateSnapshotKey("part "+part.ID, part.Key, clock, applied)
		if err != nil {
			return err
		}
		if partIndex > 0 && !setKeyBefore(previousPartKey, key) {
			return fmt.Errorf("parts are not in strict key order at %q", part.ID)
		}
		previousPartKey = key
		if key.initial {
			if key.pos != partIndex || part.ID != InitialPartID(key.pos) {
				return fmt.Errorf("part %q has an invalid initial key", part.ID)
			}
		} else {
			if !snapshot.Type.isMulti() || part.ID != AddedPartID(key.stamp.SiteID, key.stamp.Seq) {
				return fmt.Errorf("part %q is not derived from its key", part.ID)
			}
		}
		if part.Type != snapshot.Type.partType() {
			return fmt.Errorf("part %q type %s does not match %s", part.ID, part.Type, snapshot.Type)
		}
		if !snapshot.Type.isMulti() && part.Deleted {
			return fmt.Errorf("simple geometry part %q is deleted", part.ID)
		}
		if err := validatePartSnapshot(part, key, snapshot.Dims, clock, applied); err != nil {
			return err
		}
	}
	return nil
}

func validatePartSnapshot(part PartSnapshot, partKey seqKey, dims int, clock uint64, applied VectorClock) error {
	switch part.Type {
	case GeometryPoint, GeometryLineString:
		if len(part.Rings) != 1 {
			return fmt.Errorf("%s part %q requires exactly one coordinate sequence", part.Type, part.ID)
		}
	case GeometryPolygon:
		if len(part.Rings) == 0 {
			return fmt.Errorf("polygon part %q requires an exterior ring", part.ID)
		}
	}

	ringIDs := make(map[string]struct{}, len(part.Rings))
	var previousRingKey seqKey
	for ringIndex, ring := range part.Rings {
		if strings.TrimSpace(ring.ID) == "" {
			return fmt.Errorf("part %q ring %d has an empty id", part.ID, ringIndex)
		}
		if _, duplicate := ringIDs[ring.ID]; duplicate {
			return fmt.Errorf("duplicate ring id %q in part %q", ring.ID, part.ID)
		}
		ringIDs[ring.ID] = struct{}{}
		key, err := validateSnapshotKey("ring "+ring.ID, ring.Key, clock, applied)
		if err != nil {
			return err
		}
		if ringIndex > 0 && !setKeyBefore(previousRingKey, key) {
			return fmt.Errorf("rings in part %q are not in strict key order at %q", part.ID, ring.ID)
		}
		previousRingKey = key
		if key.initial {
			if key.pos != ringIndex {
				return fmt.Errorf("ring %q has an invalid initial position", ring.ID)
			}
			expected := InitialRingID(partKey.pos, key.pos)
			if !partKey.initial {
				expected = addPartRingID(partKey.stampRef(), key.pos)
			}
			if ring.ID != expected {
				return fmt.Errorf("ring %q is not derived from its key", ring.ID)
			}
		} else if part.Type != GeometryPolygon || ring.ID != AddedRingID(key.stamp.SiteID, key.stamp.Seq) {
			return fmt.Errorf("ring %q is not derived from its key", ring.ID)
		}
		exterior := part.Type == GeometryPolygon && ringIndex == 0
		if ring.Exterior != exterior {
			return fmt.Errorf("ring %q has inconsistent exterior status", ring.ID)
		}
		if ring.Exterior && ring.Deleted {
			return fmt.Errorf("exterior ring %q is deleted", ring.ID)
		}
		if err := validateRingSnapshot(ring, partKey, key, dims, clock, applied, part.Type); err != nil {
			return err
		}
	}
	return nil
}

func validateRingSnapshot(ring RingSnapshot, partKey, ringKey seqKey, dims int, clock uint64, applied VectorClock, partType GeometryType) error {
	seq := newVertexSeq()
	siblingKeys := make(map[string]map[seqKey]struct{})
	visible := 0
	for index, vertex := range ring.Vertices {
		if strings.TrimSpace(vertex.ID) == "" {
			return fmt.Errorf("ring %q vertex %d has an empty id", ring.ID, index)
		}
		if vertex.Parent != "" && !seq.has(vertex.Parent) {
			return fmt.Errorf("ring %q vertex %q precedes parent %q", ring.ID, vertex.ID, vertex.Parent)
		}
		key, err := validateSnapshotKey("vertex "+vertex.ID, vertex.Key, clock, applied)
		if err != nil {
			return err
		}
		expectedID := InsertedVertexID(key.stamp.SiteID, key.stamp.Seq)
		if key.initial {
			if ringKey.initial {
				expectedID = InitialVertexID(ringKey.pos, key.pos)
				if !partKey.initial {
					expectedID = addPartVertexID(partKey.stampRef(), ringKey.pos, key.pos)
				}
			} else {
				expectedID = addRingVertexID(ringKey.stampRef(), key.pos)
			}
			expectedParent := ""
			if key.pos > 0 {
				if ringKey.initial {
					expectedParent = InitialVertexID(ringKey.pos, key.pos-1)
					if !partKey.initial {
						expectedParent = addPartVertexID(partKey.stampRef(), ringKey.pos, key.pos-1)
					}
				} else {
					expectedParent = addRingVertexID(ringKey.stampRef(), key.pos-1)
				}
			}
			if vertex.Parent != expectedParent {
				return fmt.Errorf("initial vertex %q has parent %q, want %q", vertex.ID, vertex.Parent, expectedParent)
			}
		}
		if vertex.ID != expectedID {
			return fmt.Errorf("vertex %q is not derived from its key", vertex.ID)
		}
		if len(vertex.Coord) != dims {
			return fmt.Errorf("vertex %q has %d coordinates, want %d", vertex.ID, len(vertex.Coord), dims)
		}
		coord, _, err := parsePosition(vertex.Coord)
		if err != nil {
			return err
		}
		if err := validateOptionalSnapshotStamp("move register for "+vertex.ID, vertex.MoveReg, clock, applied); err != nil {
			return err
		}
		if siblingKeys[vertex.Parent] == nil {
			siblingKeys[vertex.Parent] = make(map[seqKey]struct{})
		}
		if _, duplicate := siblingKeys[vertex.Parent][key]; duplicate {
			return fmt.Errorf("vertices under %q have duplicate ordering keys", vertex.Parent)
		}
		siblingKeys[vertex.Parent][key] = struct{}{}
		if !seq.insert(vertex.ID, vertex.Parent, key, coord) {
			return fmt.Errorf("duplicate vertex id %q", vertex.ID)
		}
		if vertex.Deleted {
			seq.delete(vertex.ID)
		} else {
			visible++
		}
	}
	canonical := make([]string, 0, len(ring.Vertices))
	seq.walk(func(element *element) { canonical = append(canonical, element.id) })
	for i, id := range canonical {
		if ring.Vertices[i].ID != id {
			return fmt.Errorf("ring %q vertices are not in canonical traversal order", ring.ID)
		}
	}
	if partType == GeometryPoint && (len(ring.Vertices) != 1 || visible != 1) {
		return fmt.Errorf("point ring %q requires one visible vertex", ring.ID)
	}
	return nil
}

func validateSnapshotKey(name string, key KeySnapshot, clock uint64, applied VectorClock) (seqKey, error) {
	if (key.Init == nil) == (key.Stamp == nil) {
		return seqKey{}, fmt.Errorf("%s key must contain exactly one of init or stamp", name)
	}
	if key.Init != nil {
		if *key.Init < 0 {
			return seqKey{}, fmt.Errorf("%s initial key is negative", name)
		}
		return initialKey(*key.Init), nil
	}
	if err := validateSnapshotStamp(name+" key", *key.Stamp, clock, applied); err != nil {
		return seqKey{}, err
	}
	return opKey(*key.Stamp), nil
}

func validateOptionalSnapshotStamp(name string, stamp *Stamp, clock uint64, applied VectorClock) error {
	if stamp == nil {
		return nil
	}
	return validateSnapshotStamp(name, *stamp, clock, applied)
}

func validateSnapshotStamp(name string, stamp Stamp, clock uint64, applied VectorClock) error {
	if err := validateIdentity(stamp.SiteID, stamp.Seq, stamp.Timestamp); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if stamp.Timestamp > clock {
		return fmt.Errorf("%s timestamp %d exceeds snapshot clock %d", name, stamp.Timestamp, clock)
	}
	if stamp.Seq > applied[stamp.SiteID] {
		return fmt.Errorf("%s sequence %d exceeds applied knowledge", name, stamp.Seq)
	}
	return nil
}

func validateSnapshotRef(name string, ref OpRef, applied VectorClock) error {
	if strings.TrimSpace(ref.SiteID) == "" || ref.Seq == 0 || ref.Seq >= MaxTimestamp {
		return fmt.Errorf("%s %s is invalid", name, ref)
	}
	if ref.Seq > applied[ref.SiteID] {
		return fmt.Errorf("%s %s exceeds applied knowledge", name, ref)
	}
	return nil
}

func invalidSnapshot(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSnapshot, fmt.Sprintf(format, args...))
}

func (key seqKey) stampRef() OpRef {
	return OpRef{SiteID: key.stamp.SiteID, Seq: key.stamp.Seq}
}
