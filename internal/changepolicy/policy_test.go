package changepolicy

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDefaultsAndOverrideRoundTrip(t *testing.T) {
	p := Default()
	if p.Review != "on" || p.Land != "github-pr" {
		t.Fatalf("defaults: %+v", p)
	}
	o := Override{Review: "off", RequiredChecks: []string{}, Reason: "Documentation only"}
	if err := ValidateOverride(o); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(o)
	var roundtrip Override
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	effective := Apply(p, roundtrip)
	if effective.Review != "off" || effective.RequiredChecks == nil || len(effective.RequiredChecks) != 0 {
		t.Fatalf("override lost after persistence: %+v %s", effective, b)
	}
	if len(SkipReasons(effective, roundtrip)) != 2 {
		t.Fatal("missing recorded skip reasons")
	}
	if len(p.RequiredChecks) != 0 {
		t.Fatal("override changed room defaults")
	}
}
func TestInvalidPoliciesAndMissingReasons(t *testing.T) {
	for _, o := range []Override{{Review: "off"}, {Review: "yes", Reason: "x"}, {Land: "push", Reason: "x"}, {RequiredChecks: []string{"go-test", "go-test"}, Reason: "x"}} {
		if ValidateOverride(o) == nil {
			t.Fatalf("accepted invalid override %+v", o)
		}
	}
	if err := ValidateOverride(Override{}); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredCheckNamesRoundTrip(t *testing.T) {
	names := []string{"unit", "go-test-touched", "frontend-if-touched"}
	policy := Default()
	policy.RequiredChecks = names
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Policy
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, policy) {
		t.Fatalf("policy changed: got %+v, want %+v", loaded, policy)
	}
	override := Override{RequiredChecks: names, Review: "off", Land: "github-pr", Reason: "Use named GitHub checks"}
	data, err = json.Marshal(override)
	if err != nil {
		t.Fatal(err)
	}
	var loadedOverride Override
	if err := json.Unmarshal(data, &loadedOverride); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loadedOverride, override) {
		t.Fatalf("override changed: got %+v, want %+v", loadedOverride, override)
	}
}
