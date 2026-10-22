package workflow

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"
)

func TestProcessShimExposesOnlyFrozenCompatibilitySurface(t *testing.T) {
	t.Setenv("HOME", "/test/home")
	t.Setenv("USER", "test-user")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("DYNA_PROCESS_TOKEN", "must-not-leak")
	t.Setenv("DYNA_PROCESS_API_KEY", "must-not-leak")
	t.Setenv("DYNA_PROCESS_SECRET", "must-not-leak")
	unsetenvForTest(t, "XDG_CONFIG_HOME")
	unsetenvForTest(t, "XDG_DATA_HOME")

	workDir := t.TempDir()
	resultJSON, err := executeWorkflowTest(t, `
'use strict';
const mutations = {};
for (const [name, mutate] of Object.entries({
  process: () => { process.platform = 'changed'; },
  env: () => { process.env.HOME = 'changed'; },
  envAddition: () => { process.env.NEW_VALUE = 'changed'; },
})) {
  try {
    mutate();
    mutations[name] = false;
  } catch (err) {
    mutations[name] = err instanceof TypeError;
  }
}
return {
  cwd: process.cwd(),
  platform: process.platform,
  arch: process.arch,
  home: process.env.HOME,
  user: process.env.USER,
  lang: process.env.LANG,
  absentAllowlist: process.env.XDG_CONFIG_HOME === undefined && process.env.XDG_DATA_HOME === undefined,
  envNames: Object.keys(process.env).sort(),
  tokenHidden: process.env.DYNA_PROCESS_TOKEN === undefined,
  keyHidden: process.env.DYNA_PROCESS_API_KEY === undefined,
  secretHidden: process.env.DYNA_PROCESS_SECRET === undefined,
  pathHidden: process.env.PATH === undefined,
  processFrozen: Object.isFrozen(process),
  envFrozen: Object.isFrozen(process.env),
  mutations,
  requireType: typeof require,
  chdirType: typeof process.chdir,
  pidType: typeof process.pid,
};`, Options{Store: mockWorkflowStore(0, 0), WorkDir: workDir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var result struct {
		CWD             string          `json:"cwd"`
		Platform        string          `json:"platform"`
		Arch            string          `json:"arch"`
		Home            string          `json:"home"`
		User            string          `json:"user"`
		Lang            string          `json:"lang"`
		AbsentAllowlist bool            `json:"absentAllowlist"`
		EnvNames        []string        `json:"envNames"`
		TokenHidden     bool            `json:"tokenHidden"`
		KeyHidden       bool            `json:"keyHidden"`
		SecretHidden    bool            `json:"secretHidden"`
		PathHidden      bool            `json:"pathHidden"`
		ProcessFrozen   bool            `json:"processFrozen"`
		EnvFrozen       bool            `json:"envFrozen"`
		Mutations       map[string]bool `json:"mutations"`
		RequireType     string          `json:"requireType"`
		ChdirType       string          `json:"chdirType"`
		PIDType         string          `json:"pidType"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatal(err)
	}

	if result.CWD != workDir {
		t.Errorf("process.cwd() = %q, want %q", result.CWD, workDir)
	}
	if result.Platform != expectedNodePlatform() {
		t.Errorf("process.platform = %q, want %q", result.Platform, expectedNodePlatform())
	}
	if result.Arch != expectedNodeArch() {
		t.Errorf("process.arch = %q, want %q", result.Arch, expectedNodeArch())
	}
	if result.Home != "/test/home" || result.User != "test-user" || result.Lang != "en_US.UTF-8" {
		t.Errorf("allowlisted env = HOME=%q USER=%q LANG=%q", result.Home, result.User, result.Lang)
	}
	if !result.AbsentAllowlist {
		t.Error("absent allowlisted variables were exposed")
	}
	wantEnvNames := []string{"HOME", "LANG", "USER"}
	if len(result.EnvNames) != len(wantEnvNames) {
		t.Errorf("process.env names = %v, want %v", result.EnvNames, wantEnvNames)
	} else {
		for i, want := range wantEnvNames {
			if result.EnvNames[i] != want {
				t.Errorf("process.env names = %v, want %v", result.EnvNames, wantEnvNames)
				break
			}
		}
	}
	if !result.TokenHidden || !result.KeyHidden || !result.SecretHidden || !result.PathHidden {
		t.Errorf("excluded environment values leaked: token=%t key=%t secret=%t path=%t", result.TokenHidden, result.KeyHidden, result.SecretHidden, result.PathHidden)
	}
	if !result.ProcessFrozen || !result.EnvFrozen {
		t.Errorf("frozen status = process:%t env:%t, want both true", result.ProcessFrozen, result.EnvFrozen)
	}
	for name, mutated := range result.Mutations {
		if !mutated {
			t.Errorf("%s mutation did not throw TypeError", name)
		}
	}
	if result.RequireType != "undefined" || result.ChdirType != "undefined" || result.PIDType != "undefined" {
		t.Errorf("unsupported Node APIs = require:%s chdir:%s pid:%s", result.RequireType, result.ChdirType, result.PIDType)
	}
}

func unsetenvForTest(t *testing.T, name string) {
	t.Helper()
	value, wasSet := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func expectedNodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

func expectedNodeArch() string {
	switch runtime.GOARCH {
	case "386":
		return "ia32"
	case "amd64":
		return "x64"
	default:
		return runtime.GOARCH
	}
}
