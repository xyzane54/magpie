package gateway

import (
	"encoding/json"
)

// rawObject is json.Unmarshal(data, &m) for m a map[string]json.RawMessage.
func rawObject(data []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := decodeJSON(data, &m)
	return m, err
}

// rawArray is json.Unmarshal(data, &a) for a a []json.RawMessage.
func rawArray(data []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	err := decodeJSON(data, &a)
	return a, err
}
