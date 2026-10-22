package config

import (
	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/home"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestToolHomeEnvironmentOwnerPolicy(t *testing.T) {
	for _, list := range [][]string{nil, {}, {"cloud-cli", "git", "helper"}, {"claude", "agy"}} {
		t.Run(strings.Join(list, "-"), func(t *testing.T) {
			m := &ConfigManager{Path: filepath.Join(t.TempDir(), "harness-config.json")}
			inherited := []string{"HOME=/owner", "DYNA_HOME_TOOLS=untrusted", "PATH=/tools", "DYNA_HOME_TOOLS=another", "DYNA_HOME_TOOLS", "KEEP=last"}
			original := append([]string(nil), inherited...)
			if list != nil {
				data, err := json.Marshal(HarnessConfig{ToolHomeBridges: list})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(m.Path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := m.ToolHomeEnvironment(inherited)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"HOME=/owner", "PATH=/tools", "KEEP=last", "DYNA_HOME_TOOLS=" + strings.Join(list, ",")}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q want %q", got, want)
			}
			if !reflect.DeepEqual(inherited, original) {
				t.Fatal("input environment changed")
			}
			if list == nil {
				if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
					t.Fatal("read created owner configuration")
				}
			}
		})
	}
}

func TestToolHomePolicyPersistsAcrossConfigUpdates(t *testing.T) {
	m := &ConfigManager{Path: filepath.Join(t.TempDir(), "harness-config.json")}
	if err := os.WriteFile(m.Path, []byte(`{"toolHomeBridges":["claude","agy"],"mcp":[],"skills":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "default", Source: skillSource(t), EnabledHarnesses: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	config, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.ToolHomeBridges, []string{"claude", "agy"}) {
		t.Fatal("owner policy lost during update")
	}
	env, err := m.ToolHomeEnvironment(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env, []string{"DYNA_HOME_TOOLS=claude,agy"}) {
		t.Fatal(env)
	}
}

func TestToolHomePolicyRejectsInvalidConfiguration(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/bin/sh", "../tool", "a,b", "tool;evil", "has space", " padded", "trailing ", "-option", "line\nbreak"} {
		t.Run(name, func(t *testing.T) {
			m := &ConfigManager{Path: filepath.Join(t.TempDir(), "harness-config.json")}
			data, err := json.Marshal(HarnessConfig{ToolHomeBridges: []string{name}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(m.Path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ToolHomeEnvironment(nil); err == nil || !strings.Contains(err.Error(), "toolHomeBridges") {
				t.Fatalf("unsafe policy accepted: %v", err)
			}
		})
	}
	m := &ConfigManager{Path: filepath.Join(t.TempDir(), "harness-config.json")}
	for _, data := range []string{`{`, `{"toolHomeBridges":"tool"}`, `{"toolHomeBridges":[1]}`} {
		if err := os.WriteFile(m.Path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ToolHomeEnvironment(nil); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	m.Path = filepath.Dir(m.Path)
	if _, err := m.ToolHomeEnvironment(nil); err == nil {
		t.Fatal("read error ignored")
	}
}

func TestToolHomeEnvironmentPrepareUsesOnlyOwnerOptIns(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "cloud-cli"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m := &ConfigManager{Path: filepath.Join(root, "harness-config.json")}
	inherited := []string{"HOME=" + filepath.Join(root, "owner"), "PATH=" + bin, "DYNA_HOME_TOOLS=missing-untrusted-tool"}
	for _, ownerList := range [][]string{nil, {"cloud-cli"}} {
		data, err := json.Marshal(HarnessConfig{ToolHomeBridges: ownerList})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(m.Path, data, 0600); err != nil {
			t.Fatal(err)
		}
		env, err := m.ToolHomeEnvironment(inherited)
		if err != nil {
			t.Fatal(err)
		}
		out, err := home.Prepare(filepath.Join(root, "native"), "/fake/dyna", env)
		if err != nil {
			t.Fatal(err)
		}
		bridgeBin := filepath.Dir(out["DYNA_BIN"])
		for _, name := range []string{"git", "dyna"} {
			if _, err := os.Stat(filepath.Join(bridgeBin, name)); err != nil {
				t.Fatal(err)
			}
		}
		_, err = os.Stat(filepath.Join(bridgeBin, "cloud-cli"))
		if len(ownerList) == 0 && !os.IsNotExist(err) || len(ownerList) != 0 && err != nil {
			t.Fatalf("owner opt-in not respected: %v", err)
		}
	}
}
