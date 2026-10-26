package openai

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

// marshalWithExtra renders base and appends unrecognized members. A recognized
// member always wins: its value is the one the conversion actually used.
// imageURLMembers flattens image_url members into the content part Extra map.
// detail is the only modeled member; it is re-nested by the encoder.
func imageURLMembers(u imageURL) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if u.Detail != "" {
		if encoded, err := json.Marshal(u.Detail); err == nil {
			out["detail"] = encoded
		}
	}
	for name, value := range u.Extra {
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splitNested reports the member name when key is "<object>.<member>".
func splitNested(key, object string) string {
	prefix := object + "."
	if len(key) > len(prefix) && key[:len(prefix)] == prefix {
		return key[len(prefix):]
	}
	return ""
}

func setRaw(m map[string]json.RawMessage, name string, value json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	m[name] = value
	return m
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
