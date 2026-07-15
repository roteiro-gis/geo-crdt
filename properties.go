package crdt

import (
	"encoding/json"
	"fmt"
	"sort"
)

// propertyState is one last-writer-wins property register. Deleted registers
// are retained as tombstones so late-arriving older writes stay suppressed.
type propertyState struct {
	value   json.RawMessage
	deleted bool
	ref     Stamp
}

func encodeProperties(properties map[string]any) (map[string]json.RawMessage, error) {
	if len(properties) == 0 {
		return nil, nil
	}
	encoded := make(map[string]json.RawMessage, len(properties))
	for key, value := range properties {
		raw, err := encodePropertyValue(value)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", key, err)
		}
		encoded[key] = raw
	}
	return encoded, nil
}

func encodePropertyValue(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal property value: %v", ErrInvalidCommand, err)
	}
	return json.RawMessage(data), nil
}

// decodeProperties returns the visible properties as JSON-compatible values.
func decodeProperties(properties map[string]propertyState) map[string]any {
	decoded := make(map[string]any, len(properties))
	for key, state := range properties {
		if state.deleted {
			continue
		}
		var value any
		// Values are validated as JSON on entry, so this cannot fail.
		if err := json.Unmarshal(state.value, &value); err != nil {
			panic(fmt.Sprintf("crdt: stored property %q is not valid JSON: %v", key, err))
		}
		decoded[key] = value
	}
	return decoded
}

func cloneProperties(properties map[string]json.RawMessage) map[string]json.RawMessage {
	if len(properties) == 0 {
		return nil
	}
	result := make(map[string]json.RawMessage, len(properties))
	for key, value := range properties {
		result[key] = cloneRawMessage(value)
	}
	return result
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	result := make(json.RawMessage, len(raw))
	copy(result, raw)
	return result
}

// canonicalJSON re-encodes a JSON value in Go's deterministic encoding
// (object keys sorted), for content hashing.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// sortedPropertyKeys returns property keys in deterministic order.
func sortedPropertyKeys[V any](properties map[string]V) []string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
