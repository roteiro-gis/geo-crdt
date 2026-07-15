package crdt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// GeoJSONFeature is the SDK's minimal GeoJSON Feature representation.
// Geometry is nil for features without geometry ("geometry": null).
type GeoJSONFeature struct {
	Type       string          `json:"type"`
	ID         string          `json:"id,omitempty"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
}

// GeoJSONFeatureCollection is the SDK's minimal GeoJSON FeatureCollection
// representation.
type GeoJSONFeatureCollection struct {
	Type     string           `json:"type"`
	Features []GeoJSONFeature `json:"features"`
}

// baseFeature is one parsed feature of a document's base state.
type baseFeature struct {
	id         ID
	geometry   json.RawMessage // nil for null geometry
	properties map[string]json.RawMessage
}

func parseFeatureCollection(data json.RawMessage) ([]baseFeature, error) {
	var collection struct {
		Type     string `json:"type"`
		Features []struct {
			Type       string          `json:"type"`
			ID         json.RawMessage `json:"id,omitempty"`
			Geometry   json.RawMessage `json:"geometry"`
			Properties map[string]any  `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &collection); err != nil {
		return nil, fmt.Errorf("%w: unmarshal feature collection: %v", ErrInvalidGeometry, err)
	}
	if collection.Type != "FeatureCollection" {
		return nil, fmt.Errorf("%w: expected GeoJSON FeatureCollection, got %q", ErrInvalidGeometry, collection.Type)
	}

	features := make([]baseFeature, 0, len(collection.Features))
	seen := make(map[ID]struct{}, len(collection.Features))
	for i, feature := range collection.Features {
		if feature.Type != "Feature" {
			return nil, fmt.Errorf("%w: feature %d has type %q, want Feature", ErrInvalidGeometry, i, feature.Type)
		}
		id, err := parseFeatureID(feature.ID, i)
		if err != nil {
			return nil, fmt.Errorf("feature %d: %w", i, err)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: duplicate feature id %q", ErrInvalidGeometry, id)
		}
		seen[id] = struct{}{}
		properties, err := encodeProperties(feature.Properties)
		if err != nil {
			return nil, fmt.Errorf("feature %s: %w", id, err)
		}
		geometry := feature.Geometry
		if isNullGeometry(geometry) {
			geometry = nil
		}
		features = append(features, baseFeature{
			id:         id,
			geometry:   cloneRawMessage(geometry),
			properties: properties,
		})
	}
	return features, nil
}

// parseFeatureID accepts GeoJSON string or number feature IDs, coercing
// numbers to their decimal string form. Missing IDs are assigned
// deterministically from the feature index.
func parseFeatureID(raw json.RawMessage, index int) (ID, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ID(fmt.Sprintf("feature:%d", index)), nil
	}
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		if strings.TrimSpace(stringID) == "" {
			return "", fmt.Errorf("%w: feature id cannot be empty", ErrInvalidGeometry)
		}
		return ID(stringID), nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return ID(number.String()), nil
	}
	return "", fmt.Errorf("%w: feature id must be a string or number", ErrInvalidGeometry)
}

func featureCollectionFromFeatures(features []Feature) GeoJSONFeatureCollection {
	sort.Slice(features, func(i, j int) bool {
		return features[i].ID < features[j].ID
	})
	collection := GeoJSONFeatureCollection{
		Type:     "FeatureCollection",
		Features: make([]GeoJSONFeature, 0, len(features)),
	}
	for _, feature := range features {
		collection.Features = append(collection.Features, GeoJSONFeature{
			Type:       "Feature",
			ID:         string(feature.ID),
			Geometry:   cloneRawMessage(feature.Geometry),
			Properties: feature.Properties,
		})
	}
	return collection
}

// --- Base lineage hashing ---

// emptyBaseHash is the lineage hash of documents created without a base.
// All fresh documents share it, so independently created empty replicas can
// merge.
var emptyBaseHash = computeBaseHash(nil)

// computeBaseHash hashes the canonical form of a document's original base
// state. Replicas may only merge when their base hashes match: shared
// operation logs are meaningless against different bases (see
// ErrBaseMismatch). Snapshots inherit the hash, so compacted replicas remain
// mergeable with their lineage.
func computeBaseHash(features []baseFeature) string {
	type canonicalFeature struct {
		ID         string          `json:"id"`
		Geometry   json.RawMessage `json:"geometry"`
		Properties json.RawMessage `json:"properties"`
	}

	sorted := make([]baseFeature, len(features))
	copy(sorted, features)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

	canonical := make([]canonicalFeature, 0, len(sorted))
	for _, feature := range sorted {
		entry := canonicalFeature{ID: string(feature.id)}
		if feature.geometry != nil {
			// Canonicalize through the geometry engine so formatting
			// differences in the source JSON do not affect the hash.
			if state, err := newGeometryState(feature.geometry); err == nil {
				entry.Geometry = state.geoJSON()
			} else {
				entry.Geometry = feature.geometry
			}
		}
		if len(feature.properties) > 0 {
			properties := make(map[string]json.RawMessage, len(feature.properties))
			for _, key := range sortedPropertyKeys(feature.properties) {
				if value, err := canonicalJSON(feature.properties[key]); err == nil {
					properties[key] = value
				} else {
					properties[key] = feature.properties[key]
				}
			}
			encoded, err := json.Marshal(properties)
			if err == nil {
				entry.Properties = encoded
			}
		}
		canonical = append(canonical, entry)
	}

	encoded, err := json.Marshal(canonical)
	if err != nil {
		// Canonical features hold only strings and raw JSON.
		panic(fmt.Sprintf("crdt: marshal canonical base: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
