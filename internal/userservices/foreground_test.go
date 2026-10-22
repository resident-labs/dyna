package userservices

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineAndAgentUnitsHaveSeparateResourcePriorities(t *testing.T) {
	dir := environment(t)
	calls := []string{}
	fakeSystemctl := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "Transient=no\nActiveState=active\nLoadState=loaded\n", nil
	}
	if err := Install(context.Background(), Options{Binary: "/test/bin/dyna", Dir: dir, Run: fakeSystemctl}); err != nil {
		t.Fatal(err)
	}
	engine, err := os.ReadFile(filepath.Join(dir, Engine))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Slice=app.slice\n", "CPUWeight=100\n", "IOWeight=100\n"} {
		if !strings.Contains(string(engine), want) {
			t.Errorf("engine missing %q: %s", want, engine)
		}
	}
	agent, err := os.ReadFile(filepath.Join(dir, "dyna.slice"))
	if err != nil {
		t.Errorf("installer did not write the agent slice: %v", err)
	} else {
		for _, want := range []string{"CPUWeight=20\n", "IOWeight=20\n"} {
			if !strings.Contains(string(agent), want) {
				t.Errorf("agent slice missing %q: %s", want, agent)
			}
		}
	}
	computers, err := os.ReadFile(filepath.Join(dir, Computers))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(computers), "Slice=dyna.slice\n") {
		t.Error("agent computer supervisor left the agent slice")
	}
	if len(calls) == 0 {
		t.Fatal("fake systemctl did not run")
	}
}
