package main

import (
	"strings"
	"testing"
)

// withoutDynaEnv removes caller settings before a fixture adds its own.
func withoutDynaEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "DYNA_") {
			out = append(out, entry)
		}
	}
	return out
}

func TestE2EEnvClearsInheritedDynaEnvAndPreservesOverrides(t *testing.T) {
	t.Setenv("DYNA_SESSION", "caller")
	t.Setenv("DYNA_FUTURE_SETTING", "caller=value")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "1")
	t.Setenv("DYNA_SLICE", "on")
	t.Setenv("ROOT_TEST_KEEP_ENV", "keep=value")
	e := &e2e{data: "test-data", config: "test-config", extra: []string{
		"DYNA_SESSION=fixture", "DYNA_COMPUTERS_ENABLED=1",
	}}
	env := e.env()
	got := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		got[key] = value
	}
	if _, ok := got["DYNA_FUTURE_SETTING"]; ok {
		t.Fatal("child environment retained an inherited Dyna setting")
	}
	for key, want := range map[string]string{
		"ROOT_TEST_KEEP_ENV": "keep=value", "XDG_DATA_HOME": e.data, "XDG_CONFIG_HOME": e.config,
		"DYNA_SESSION": "fixture", "DYNA_COMPUTERS_ENABLED": "1", "DYNA_NO_AUTO_UPDATE": "1", "DYNA_SLICE": "off",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	e.extra = nil
	for _, entry := range e.env() {
		if entry == "DYNA_COMPUTERS_ENABLED=0" {
			return
		}
	}
	t.Fatal("child environment lost the computer integration default")
}
