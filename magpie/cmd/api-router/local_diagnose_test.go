package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalDiagnosticsRejectArbitraryInputsWithoutStartup(t *testing.T) {
	for _, args := range [][]string{{"--prompt", "bypass"}, {"--endpoint", "http://127.0.0.1:1"}, {"--dll-directory", "C:/arbitrary"}, {"--capability", "available"}, {"--generation-timeout", "30m"}, {"unexpected"}} {
		root := t.TempDir()
		data := filepath.Join(root, "data")
		command := append([]string{"diagnose-local", "--data-dir", data, "--output", filepath.Join(root, "evidence", "result.json")}, args...)
		var out bytes.Buffer
		if err := run(command, &out); err == nil {
			t.Fatalf("accepted arbitrary diagnostic input: %v", args)
		}
		if _, err := os.Stat(data); !os.IsNotExist(err) {
			t.Fatalf("invalid diagnostic input created product data: %v", err)
		}
	}
}

func TestLocalDiagnosticsHelpDoesNotInitializeProduct(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"diagnose-local", "--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("model-file")) {
		t.Fatal("diagnostic help omitted fixed cache options", out.String())
	}
}
