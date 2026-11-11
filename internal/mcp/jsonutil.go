package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
)

// field is one key/value of a JSON object, kept in document order. The
// extension's results are passed through to the agent; re-marshalling them
// as Go maps would sort the keys, so objects are edited as ordered fields.
type field struct {
	Key   string
	Value json.RawMessage
}

func objectFields(raw json.RawMessage) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not an object")
	}
	var out []field
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, field{kt.(string), v})
	}
	return out, nil
}

func encodeFields(fs []field) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(compact(f.Value))
	}
	b.WriteByte('}')
	return b.Bytes()
}

func compact(raw json.RawMessage) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}

// withoutKeys drops keys from a JSON object, keeping the others in order.
func withoutKeys(raw json.RawMessage, keys ...string) (json.RawMessage, error) {
	fs, err := objectFields(raw)
	if err != nil {
		return nil, err
	}
	out := fs[:0]
	for _, f := range fs {
		drop := false
		for _, k := range keys {
			if f.Key == k {
				drop = true
			}
		}
		if !drop {
			out = append(out, f)
		}
	}
	return encodeFields(out), nil
}

// marshal is json.Marshal without HTML escaping (JSON.stringify doesn't escape <, >, &).
func marshal(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return string(bytes.TrimRight(b.Bytes(), "\n"))
}

// canonical returns a key-sorted encoding, for comparing JSON values.
func canonical(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return marshal(v)
}
