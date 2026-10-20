package cloudcodec

import (
	"bytes"
	"encoding/json"
)

// marshalPlain is json.Marshal leaving <, > and & as they are.
func MarshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
