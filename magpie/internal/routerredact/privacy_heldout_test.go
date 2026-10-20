package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestPrivacyHeldoutAnnotationsAreIndependentAndExact(t *testing.T) {
	spec := privacyTestSpecification(t)
	cases, err := PrivacyHeldoutCases(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 10 {
		t.Fatal("heldout material case count changed without version review")
	}
	original, err := PrivacyEvaluationCases(spec)
	if err != nil {
		t.Fatal(err)
	}
	oldTexts := map[string]bool{}
	for _, c := range original {
		for _, text := range c.Texts {
			oldTexts[text] = true
		}
	}
	entities, negatives := 0, 0
	for _, c := range cases {
		if c.Versions != spec.Versions() {
			t.Fatal("heldout material used a different recognition version")
		}
		got, err := privacyExpectedEntities(context.Background(), c, PrivacyLabels())
		if err != nil || len(got) != len(c.Expected) {
			t.Fatal("heldout annotation is invalid")
		}
		entities += len(got)
		if len(got) == 0 {
			negatives++
		}
		for _, want := range c.Expected {
			if want.Text == "" {
				t.Fatal("heldout annotation must pin its exact synthetic entity")
			}
		}
		for _, text := range c.Texts {
			if oldTexts[text] {
				t.Fatal("heldout material repeats a tuning corpus field")
			}
		}
	}
	if entities != 11 || negatives != 2 || len(cases[6].Texts) != 17 {
		t.Fatal("heldout material lost its entity, negative or batch-boundary coverage")
	}
	if len(PrivacyLabels()) != 49 {
		t.Fatal("heldout measurement changed the fixed label set")
	}
}

func TestPrivacyHeldoutInputsRemainPrivateAndCallerOwned(t *testing.T) {
	spec := privacyTestSpecification(t)
	cases, err := PrivacyHeldoutCases(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		for _, text := range c.Texts {
			if text != "" && bytes.Contains(raw, []byte(text)) {
				t.Fatal("ordinary diagnostic serialization contains a heldout input")
			}
		}
	}
	cases[6].Texts[16] = "mutated"
	cases[2].Expected[0].Text = "mutated"
	again, err := PrivacyHeldoutCases(spec)
	if err != nil {
		t.Fatal(err)
	}
	if again[6].Texts[16] == "mutated" || again[2].Expected[0].Text == "mutated" {
		t.Fatal("a diagnostic caller changed subsequent heldout material")
	}
	if _, err := PrivacyHeldoutCases(nil); err == nil {
		t.Fatal("heldout material accepted an unbound recognition specification")
	}
}
