package anthropic

import "encoding/json"

// Passthrough helpers shared by every wire shape in this package. They are
// duplicated per adapter on purpose: adapters may only depend on ir, never on
// each other.

func knownSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

func unknownMembers(data []byte, known map[string]bool) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	for name, value := range all {
		if known[name] {
			continue
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[name] = value
	}
	return extra, nil
}

func marshalWithExtra(base any, extra map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, exists := merged[name]; !exists {
			merged[name] = value
		}
	}
	return json.Marshal(merged)
}
