package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExperimentalServeIsExplicitAndCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := runContext(ctx, []string{"serve", "--experimental-redaction"}, &out); !errors.Is(err, context.Canceled) {
		t.Fatal("explicit trial serve was not admitted to canceled service startup", err)
	}
	for _, args := range [][]string{{"serve", "--experimental-redaction=true"}, {"serve", "--experimental-redaction", "--addr", "0.0.0.0:13145"}, {"healthcheck", "--experimental-redaction"}} {
		if err := runContext(ctx, args, &out); err == nil || !strings.Contains(err.Error(), "disabled in api-router") {
			t.Fatal("unrecognized trial/serve options were accepted", args, err)
		}
	}
}
