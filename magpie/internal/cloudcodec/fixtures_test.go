package cloudcodec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "gateway", "testdata", "workdir", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func withModel(body []byte, model string) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		panic(err)
	}
	object["model"], _ = json.Marshal(model)
	body, _ = json.Marshal(object)
	return body
}
