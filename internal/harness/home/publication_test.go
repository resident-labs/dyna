package home

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type savedArtifact struct {
	path     string
	info     os.FileInfo
	contents []byte
}

func saveArtifacts(t *testing.T, out map[string]string, tools ...string) []savedArtifact {
	t.Helper()
	paths := []string{out["GIT_CONFIG_GLOBAL"], out["DYNA_BIN"], filepath.Join(filepath.Dir(out["DYNA_BIN"]), "git")}
	for _, name := range tools {
		paths = append(paths, filepath.Join(filepath.Dir(out["DYNA_BIN"]), name))
	}
	var result []savedArtifact
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, savedArtifact{path, info, data})
	}
	return result
}
func assertArtifactsUnchanged(t *testing.T, saved []savedArtifact) {
	t.Helper()
	for _, old := range saved {
		info, err := os.Stat(old.path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(old.path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(old.info, info) || old.info.Mode() != info.Mode() || !old.info.ModTime().Equal(info.ModTime()) || !bytes.Equal(old.contents, data) {
			t.Fatalf("active artifact changed: %s", old.path)
		}
	}
}

func TestConcurrentDifferentPoliciesKeepEarlierPublications(t *testing.T) {
	scope, owner, env := fixture(t)
	bin := envMap(env)["PATH"]
	for _, name := range []string{"cloud", "helper"} {
		write(t, filepath.Join(bin, name), "#!/bin/sh\nprintf '%s' \"$HOME\"\n", 0700)
	}
	first, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=cloud"))
	if err != nil {
		t.Fatal(err)
	}
	saved := saveArtifacts(t, first, "cloud")
	type prepared struct {
		policy string
		env    map[string]string
		err    error
	}
	results := make(chan prepared, 16)
	for i := range 16 {
		policy := []string{"cloud", "helper"}[i%2]
		go func() {
			out, err := Prepare(scope, "", append(append([]string(nil), env...), "DYNA_HOME_TOOLS="+policy))
			results <- prepared{policy, out, err}
		}()
	}
	paths := make(map[string]string)
	for range 16 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if previous, ok := paths[result.policy]; ok && previous != result.env["DYNA_BIN"] {
			t.Fatal("same policy returned different publications")
		}
		paths[result.policy] = result.env["DYNA_BIN"]
		if got := run(t, merged(env, result.env), filepath.Join(filepath.Dir(result.env["DYNA_BIN"]), result.policy)); got != owner {
			t.Fatal(got)
		}
	}
	if paths["cloud"] == paths["helper"] || paths["cloud"] != first["DYNA_BIN"] {
		t.Fatal("policies share or lose their immutable directory")
	}
	defaults, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cloud", "helper"} {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(defaults["DYNA_BIN"]), name)); !os.IsNotExist(err) {
			t.Fatalf("new default policy exposes %s", name)
		}
	}
	assertArtifactsUnchanged(t, saved)
	if got := run(t, merged(env, first), filepath.Join(filepath.Dir(first["DYNA_BIN"]), "cloud")); got != owner {
		t.Fatal("earlier active bridge no longer works")
	}
	relative, err := filepath.Rel(first["HOME"], first["DYNA_BIN"])
	if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		t.Fatal("DYNA_BIN must stay under private HOME")
	}
}

func TestOwnerEnvironmentSelectsImmutablePublication(t *testing.T) {
	scope, _, env := fixture(t)
	first, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	saved := saveArtifacts(t, first)
	for _, change := range []string{"HOME=/another/owner", "XDG_DATA_HOME=", "XDG_CONFIG_HOME=/other/config", "GIT_CONFIG_GLOBAL=/owner/selected-global"} {
		next, err := Prepare(scope, "", append(env, change))
		if err != nil {
			t.Fatal(err)
		}
		if next["DYNA_BIN"] == first["DYNA_BIN"] || next["GIT_CONFIG_GLOBAL"] == first["GIT_CONFIG_GLOBAL"] {
			t.Fatalf("owner input reused prior set: %s", change)
		}
	}
	for _, change := range []string{"DYNA_SESSION=another-turn", "DYNA_ADDRESS=another-address", "UNRELATED_CREDENTIAL=changed", "OWNER_INPUT=changed"} {
		next, err := Prepare(scope, "", append(env, change))
		if err != nil {
			t.Fatal(err)
		}
		if next["DYNA_BIN"] != first["DYNA_BIN"] || next["GIT_CONFIG_GLOBAL"] != first["GIT_CONFIG_GLOBAL"] {
			t.Fatalf("unrelated owner input created another set: %s", change)
		}
	}
	reversed := append([]string(nil), env...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	reused, err := Prepare(scope, "", reversed)
	if err != nil {
		t.Fatal(err)
	}
	if reused["DYNA_BIN"] != first["DYNA_BIN"] {
		t.Fatal("environment ordering changed publication")
	}
	assertArtifactsUnchanged(t, saved)
	// Hashes must preserve unset versus empty, even when fallback roots match.
	empty, err := Prepare(scope, "", append(env, "XDG_DATA_HOME="))
	if err != nil {
		t.Fatal(err)
	}
	if empty["DYNA_BIN"] == first["DYNA_BIN"] {
		t.Fatal("unset and empty owner values collapsed")
	}
}

func TestDamagedPublicationFailsWithoutRepair(t *testing.T) {
	scope, _, env := fixture(t)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	write(t, out["DYNA_BIN"], "changed", 0700)
	_, err = Prepare(scope, "", env)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("damaged publication reused: %v", err)
	}
	publication := filepath.Dir(out["GIT_CONFIG_GLOBAL"])
	recovery := fmt.Sprintf("remove the entire publication directory %q", publication)
	if !strings.Contains(err.Error(), recovery) {
		t.Fatalf("error must identify the whole publication for recovery: %v", err)
	}
	data, err := os.ReadFile(out["DYNA_BIN"])
	if err != nil || string(data) != "changed" {
		t.Fatal("existing publication was rewritten")
	}
}

func TestPrivatePublicationCannotBecomeRealTool(t *testing.T) {
	scope, _, env := fixture(t)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(scope, out["DYNA_BIN"], env); err == nil {
		t.Fatal("private bridge accepted as real dyna")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(out["DYNA_BIN"], alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(scope, alias, env); err == nil {
		t.Fatal("symlink alias bypassed recursion check")
	}
}

func TestCanonicalToolPolicyWinsFinalMerge(t *testing.T) {
	scope, _, env := fixture(t)
	bin := envMap(env)["PATH"]
	for _, name := range []string{"cloud", "helper"} {
		write(t, filepath.Join(bin, name), "#!/bin/sh\nexit 0\n", 0700)
	}
	first, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=helper,git,cloud,helper,dyna"))
	if err != nil {
		t.Fatal(err)
	}
	if first["DYNA_HOME_TOOLS"] != "cloud,helper" {
		t.Fatalf("noncanonical optional policy: %q", first["DYNA_HOME_TOOLS"])
	}
	profile := append(env, "DYNA_HOME_TOOLS=untrusted")
	if got := envMap(merged(profile, first))["DYNA_HOME_TOOLS"]; got != "cloud,helper" {
		t.Fatalf("profile policy survived final merge: %q", got)
	}
	equivalent, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=cloud,helper"))
	if err != nil {
		t.Fatal(err)
	}
	if first["DYNA_BIN"] != equivalent["DYNA_BIN"] {
		t.Fatal("equivalent tool policy created another publication")
	}
	defaults, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	value, present := defaults["DYNA_HOME_TOOLS"]
	if !present || value != "" {
		t.Fatal("default policy must explicitly clear optional inherited tools")
	}
	if got := envMap(merged(profile, defaults))["DYNA_HOME_TOOLS"]; got != "" {
		t.Fatal("default policy did not replace profile policy")
	}
}
