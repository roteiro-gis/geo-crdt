package crdt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RebaseReceipt describes an epoch transition. Rebase changes the document
// namespace, so deltas and actor sequences from the previous epoch cannot be
// accepted by the replacement state.
type RebaseReceipt struct {
	PreviousDocumentID DocumentID  `json:"previous_document_id"`
	DocumentID         DocumentID  `json:"document_id"`
	PreviousBaseHash   string      `json:"previous_base_hash"`
	BaseHash           string      `json:"base_hash"`
	StableClock        VectorClock `json:"stable_clock"`
	Snapshot           Snapshot    `json:"snapshot"`
}

// Rebase replaces causally stable CRDT history with the current visible state
// as a new base in a new document namespace. The caller must coordinate all
// replicas and pass a stable clock that every participant has durably
// observed. This replica verifies that the asserted clock exactly matches its
// contiguous knowledge and refuses to discard delivery gaps, dependency
// buffers, or an unsent local outbox.
//
// The returned snapshot is the bootstrap artifact for the new epoch. Peers
// must stop publishing the previous DocumentID before accepting it.
func (d *Document) Rebase(newDocumentID DocumentID, stableClock VectorClock, snapshotID string) (RebaseReceipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	newDocumentID = DocumentID(strings.TrimSpace(string(newDocumentID)))
	if newDocumentID == "" {
		return RebaseReceipt{}, fmt.Errorf("%w: new document_id is required", ErrRebaseUnsafe)
	}
	if newDocumentID == d.documentID {
		return RebaseReceipt{}, fmt.Errorf("%w: new document_id must identify a new epoch", ErrRebaseUnsafe)
	}
	if err := validateWireVectorClock("stable_clock", stableClock); err != nil {
		return RebaseReceipt{}, fmt.Errorf("%w: %v", ErrRebaseUnsafe, err)
	}

	knowledge := d.knowledgeLocked()
	if !stableClock.coveredBy(knowledge) || !knowledge.coveredBy(stableClock) {
		return RebaseReceipt{}, fmt.Errorf(
			"%w: stable clock does not equal local contiguous knowledge", ErrRebaseUnsafe)
	}
	if len(d.frontier.staged) != 0 {
		return RebaseReceipt{}, fmt.Errorf("%w: delivery gaps remain", ErrRebaseUnsafe)
	}
	if d.syncedThrough != d.localSeq {
		return RebaseReceipt{}, fmt.Errorf(
			"%w: local operations through %d are not acknowledged", ErrRebaseUnsafe, d.localSeq)
	}
	if len(d.pendingGen) != 0 {
		return RebaseReceipt{}, fmt.Errorf("%w: geometry generations remain unresolved", ErrRebaseUnsafe)
	}
	for featureID, feature := range d.features {
		if feature.geometry != nil && len(feature.geometry.pending) != 0 {
			return RebaseReceipt{}, fmt.Errorf(
				"%w: feature %q has unresolved geometry dependencies", ErrRebaseUnsafe, featureID)
		}
	}

	baseFeatures, replacement, err := d.rebaseStateLocked()
	if err != nil {
		return RebaseReceipt{}, err
	}

	previousDocumentID := d.documentID
	previousBaseHash := d.baseHash
	baseHash := computeBaseHash(newDocumentID, baseFeatures)

	d.documentID = newDocumentID
	d.clock = 0
	d.localSeq = 0
	d.baseHash = baseHash
	d.features = replacement
	d.ops = nil
	d.seen = make(map[OpRef]payloadHash)
	d.frontier = newFrontierClock()
	d.compacted = make(VectorClock)
	d.syncedThrough = 0
	d.issuedThrough = 0
	d.pendingGen = make(map[OpRef][]DocumentOp)

	snapshot, err := d.snapshotLocked(snapshotID)
	if err != nil {
		return RebaseReceipt{}, err
	}
	return RebaseReceipt{
		PreviousDocumentID: previousDocumentID,
		DocumentID:         newDocumentID,
		PreviousBaseHash:   previousBaseHash,
		BaseHash:           baseHash,
		StableClock:        cloneVectorClock(stableClock),
		Snapshot:           snapshot,
	}, nil
}

// rebaseStateLocked constructs the replacement base without changing the
// document. Re-parsing visible geometry proves that the new base satisfies
// structural invariants before the epoch transition is committed.
func (d *Document) rebaseStateLocked() ([]baseFeature, map[ID]*featureState, error) {
	ids := make([]ID, 0, len(d.features))
	for id, feature := range d.features {
		if feature.visible() {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	baseFeatures := make([]baseFeature, 0, len(ids))
	replacement := make(map[ID]*featureState, len(ids))
	for _, id := range ids {
		current := d.features[id]
		base := baseFeature{
			id:         id,
			properties: make(map[string]json.RawMessage),
		}
		for key, property := range current.properties {
			if !property.deleted {
				base.properties[key] = cloneRawMessage(property.value)
			}
		}
		if len(base.properties) == 0 {
			base.properties = nil
		}
		if current.geometry != nil {
			base.geometry = current.geometry.geoJSON()
		}

		state := &featureState{
			id:         id,
			isBase:     true,
			seenGens:   map[OpRef]struct{}{{}: {}},
			properties: make(map[string]propertyState, len(base.properties)),
		}
		if base.geometry != nil {
			geometry, err := newGeometryState(base.geometry)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"%w: feature %q cannot form a valid base: %v", ErrRebaseUnsafe, id, err)
			}
			state.geometry = geometry
		}
		for key, value := range base.properties {
			state.properties[key] = propertyState{value: cloneRawMessage(value)}
		}
		baseFeatures = append(baseFeatures, base)
		replacement[id] = state
	}
	return baseFeatures, replacement, nil
}
