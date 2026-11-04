package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"time"

	"context"
	"encoding/json"
	"errors"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

type bundleSkill struct{ name, description string }

func writeBundle(t *testing.T, path string, items []contract.Addon, folders ...bundleSkill) {
	t.Helper()
	archive := skills.NewArchive()
	list, err := json.Marshal(skills.List{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Add(skills.ListFile, 0o600, list); err != nil {
		t.Fatal(err)
	}
	for _, skill := range folders {
		if err := archive.Add(skill.name+"/"+skills.File, 0o600, []byte("---\nname: "+skill.name+"\ndescription: "+skill.description+"\n---\n")); err != nil {
			t.Fatal(err)
		}
	}
	data, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type addonBox struct {
	addonStart
	root     string
	installs atomic.Int32
}

func newAddonBox(t *testing.T) *addonBox {
	root := t.TempDir()
	b := &addonBox{root: root}
	b.addonStart = addonStart{Bundles: []string{filepath.Join(root, "global.tgz"), filepath.Join(root, "room.tgz")}, Skills: filepath.Join(root, "pi", "skills"), Settings: filepath.Join(root, "pi", "settings.json"), Cache: filepath.Join(root, "cache")}
	b.Install = func(_ context.Context, dir, source string) error {
		b.installs.Add(1)
		if strings.Contains(source, "broken") {
			return errors.New("npm said no")
		}
		return os.MkdirAll(filepath.Join(dir, "npm", "node_modules", "tools"), 0o700)
	}
	if err := os.MkdirAll(filepath.Join(root, "pi"), 0o700); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *addonBox) settings(t *testing.T) (trust string, packages []string) {
	t.Helper()
	data, err := os.ReadFile(b.Settings)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		DefaultProjectTrust string   `json:"defaultProjectTrust"`
		SteeringMode        string   `json:"steeringMode"`
		Packages            []string `json:"packages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	// Messages that arrive during one tool call reach the model together.
	if parsed.SteeringMode != "all" {
		t.Fatalf("steering mode %q", parsed.SteeringMode)
	}
	return parsed.DefaultProjectTrust, parsed.Packages
}

func TestABoxGetsEveryScopeAndTheRoomWinsAClash(t *testing.T) {
	b := newAddonBox(t)
	skill := func(name string) contract.Addon { return contract.Addon{Kind: contract.AddonSkill, Name: name} }
	writeBundle(t, b.Bundles[0], []contract.Addon{skill("shared"), skill("clash"), {Kind: contract.AddonMCP, Name: "docs", URL: "https://global.test/mcp"}, {Kind: contract.AddonMCP, Name: "only-global", Command: "server"}}, bundleSkill{"shared", "global"}, bundleSkill{"clash", "global"})
	writeBundle(t, b.Bundles[1], []contract.Addon{skill("clash"), {Kind: contract.AddonMCP, Name: "docs", URL: "https://room.test/mcp"}}, bundleSkill{"clash", "room"})
	// A file left from an earlier start goes.
	if err := os.MkdirAll(filepath.Join(b.Skills, "removed-since"), 0o700); err != nil {
		t.Fatal(err)
	}
	servers, problems, err := b.apply(context.Background())
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	if servers["docs"].URL != "https://room.test/mcp" || servers["docs"].Exposure != "deferred" || servers["only-global"].Command != "server" {
		t.Fatalf("%+v", servers)
	}
	for _, name := range []string{"dyna-extend", "shared", "clash"} {
		if _, err := os.Stat(filepath.Join(b.Skills, name, skills.File)); err != nil {
			t.Error(err)
		}
	}
	clash, err := os.ReadFile(filepath.Join(b.Skills, "clash", skills.File))
	if err != nil || !strings.Contains(string(clash), "description: room") {
		t.Fatalf("%q %v", clash, err)
	}
	if _, err := os.Stat(filepath.Join(b.Skills, "removed-since")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an old skill stayed")
	}
	if trust, packages := b.settings(t); trust != "always" || len(packages) != 0 {
		t.Fatal(trust, packages)
	}
}

func TestABoxWithNothingAddedStillHasTheBuiltIns(t *testing.T) {
	b := newAddonBox(t)
	servers, problems, err := b.apply(context.Background())
	if err != nil || len(problems) != 0 || len(servers) != 0 {
		t.Fatalf("%v %v %v", servers, problems, err)
	}
	if _, err := os.Stat(filepath.Join(b.Skills, "dyna-extend", skills.File)); err != nil {
		t.Fatal(err)
	}
}

func TestAPackageInstallsOncePerHostAndAFailureNamesItself(t *testing.T) {
	b := newAddonBox(t)
	good := contract.Addon{Kind: contract.AddonPackage, Name: "tools", Source: "npm:tools@1.0.0"}
	bad := contract.Addon{Kind: contract.AddonPackage, Name: "broken", Source: "npm:broken@1.0.0"}
	writeBundle(t, b.Bundles[1], []contract.Addon{good, bad})
	_, problems, err := b.apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "npm:broken@1.0.0") || !strings.Contains(problems[0], "npm said no") {
		t.Fatalf("%q", problems)
	}
	if !strings.Contains(addonNotice(problems), "did not load at this box start") || addonNotice(nil) != "" {
		t.Fatal(addonNotice(problems))
	}
	_, packages := b.settings(t)
	if len(packages) != 1 || !strings.HasSuffix(packages[0], "/npm/node_modules/tools") {
		t.Fatal(packages)
	}
	if _, err := os.Stat(packages[0]); err != nil {
		t.Fatal(err)
	}
	installs := b.installs.Load()
	// The next box finds the good one in the cache and tries the bad one again.
	if _, _, err := b.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := b.installs.Load(); got != installs+1 {
		t.Fatalf("%d installs after %d", got, installs)
	}
	entries, err := os.ReadDir(b.Cache)
	if err != nil || len(entries) != 1 {
		t.Fatalf("%v %v", entries, err)
	}
}

func TestPinnedVersionsOfOnePackageKeepTheirOwnFolders(t *testing.T) {
	b := newAddonBox(t)
	one, err := b.installOne(context.Background(), contract.Addon{Name: "tools", Source: "npm:tools@1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := b.installOne(context.Background(), contract.Addon{Name: "tools", Source: "npm:tools@2.0.0"})
	if err != nil || one == two {
		t.Fatalf("%s %s %v", one, two, err)
	}
}

func TestABrokenBundleCostsANotice(t *testing.T) {
	b := newAddonBox(t)
	if err := os.WriteFile(b.Bundles[0], []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeBundle(t, b.Bundles[1], []contract.Addon{{Kind: contract.AddonSkill, Name: "room-skill"}}, bundleSkill{"room-skill", "room"})
	_, problems, err := b.apply(context.Background())
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "global.tgz") {
		t.Fatalf("%q %v", problems, err)
	}
	if _, err := os.Stat(filepath.Join(b.Skills, "room-skill", skills.File)); err != nil {
		t.Fatal(err)
	}
}

func TestLastOutputKeepsTheEnd(t *testing.T) {
	if got := lastOutput([]byte(strings.Repeat("x", 500) + " the reason\n")); !strings.HasSuffix(got, "the reason") || len(got) > 410 {
		t.Fatalf("%q", got)
	}
}

func TestAHostSkillSaysWhereItsCommandsRun(t *testing.T) {
	b := newAddonBox(t)
	writeBundle(t, b.Bundles[1], []contract.Addon{{Kind: contract.AddonSkill, Name: "hypruse", Hosts: []contract.HostName{"omarchy", "desktop", "arch"}}, {Kind: contract.AddonSkill, Name: "plain"}}, bundleSkill{"hypruse", "Drive Hyprland"}, bundleSkill{"plain", "Plain"})
	if _, problems, err := b.apply(context.Background()); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	data, err := os.ReadFile(filepath.Join(b.Skills, "hypruse", skills.File))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := skills.Parse(data)
	if err != nil || meta.Name != "hypruse" || meta.Description != "Drive Hyprland · runs on omarchy, desktop or arch" {
		t.Fatalf("%+v %v", meta, err)
	}
	if !strings.HasSuffix(string(data), "\n---\nThis skill's commands run on a host: omarchy, desktop or arch, through bash with host. The owner's messages say \"on <host>\" when sent from one of them. Files written to $DYNA_OUT come back to this box.\n") {
		t.Fatalf("%q", data)
	}
	plain, err := os.ReadFile(filepath.Join(b.Skills, "plain", skills.File))
	if err != nil || strings.Contains(string(plain), "host") {
		t.Fatalf("%q %v", plain, err)
	}
}

func TestOnlyARootBoxReadsTheRootOnlyBundles(t *testing.T) {
	if got := roomAddons(false).Bundles; len(got) != 2 || strings.Contains(strings.Join(got, " "), "root-addons") {
		t.Fatalf("%v", got)
	}
	if got := roomAddons(true).Bundles; len(got) != 4 || !strings.Contains(strings.Join(got, " "), "/run/dyna/root-addons/room/") {
		t.Fatalf("%v", got)
	}
}

// registry serves one npm package, the fixture folder packed as its tarball, as the registry the box's npm talks to.
func registry(t *testing.T, folder string, hang <-chan struct{}) {
	t.Helper()
	var tarball bytes.Buffer
	zipped := gzip.NewWriter(&tarball)
	archive := tar.NewWriter(zipped)
	err := filepath.WalkDir(folder, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(folder, path)
		if err != nil {
			return err
		}
		if err := archive.WriteHeader(&tar.Header{Name: "package/" + filepath.ToSlash(name), Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = archive.Write(data)
		return err
	})
	if err != nil || archive.Close() != nil || zipped.Close() != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang != nil {
			<-hang
		}
		if r.Method == http.MethodPost { // npm's audit
			_, _ = w.Write([]byte("{}"))
			return
		}
		if strings.HasSuffix(r.URL.Path, ".tgz") {
			_, _ = w.Write(tarball.Bytes())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":      "fixture-package",
			"dist-tags": map[string]string{"latest": "1.0.0"},
			"versions":  map[string]any{"1.0.0": map[string]any{"name": "fixture-package", "version": "1.0.0", "dist": map[string]string{"tarball": server.URL + "/fixture-package/-/fixture-package-1.0.0.tgz"}}},
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("npm_config_registry", server.URL)
}

func fixturePackage() contract.Addon {
	return contract.Addon{Kind: contract.AddonPackage, Name: "fixture-package", Source: "npm:fixture-package@1.0.0"}
}

// The install goes through PiG, so a box with no pi binary installs a package, and its skill and prompt are there.
func TestAPackageInstallsWithoutAPiBinary(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pi-ran")
	shims := t.TempDir()
	if err := os.WriteFile(filepath.Join(shims, "pi"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	registry(t, pigtest.Package(t, pigtest.ToolExtension("fixture_tool", "ok")), nil)
	b := newAddonBox(t)
	b.Install = piInstall
	writeBundle(t, b.Bundles[1], []contract.Addon{fixturePackage()})
	_, problems, err := b.apply(context.Background())
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the pi binary ran")
	}
	_, packages := b.settings(t)
	if len(packages) != 1 {
		t.Fatal(packages)
	}
	for _, file := range []string{"package.json", "skills/fixture-skill/SKILL.md", "prompts/fixture-prompt.md", "extensions/index.js"} {
		if _, err := os.Stat(filepath.Join(packages[0], file)); err != nil {
			t.Error(err)
		}
	}
}

// A package whose extension throws is a problem for the notice, stays out of the settings, and is not cached, so the next
// box start tries it again.
func TestAPackageWhoseExtensionFailsToLoadIsAProblemAndStaysOut(t *testing.T) {
	registry(t, pigtest.Package(t, `throw new Error("no good");`), nil)
	b := newAddonBox(t)
	b.Install = piInstall
	writeBundle(t, b.Bundles[1], []contract.Addon{fixturePackage()})
	_, problems, err := b.apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "npm:fixture-package@1.0.0") || !strings.Contains(problems[0], "its extension failed to load") || !strings.Contains(problems[0], "no good") {
		t.Fatalf("%q", problems)
	}
	if _, packages := b.settings(t); len(packages) != 0 {
		t.Fatal(packages)
	}
	if entries, err := os.ReadDir(b.Cache); err != nil || len(entries) != 0 {
		t.Fatalf("%v %v", entries, err)
	}
}

// A package that cannot be installed is a problem that says why.
func TestAPackageThatFailsToInstallIsAProblemThatSaysWhy(t *testing.T) {
	registry(t, pigtest.Package(t, ""), nil)
	b := newAddonBox(t)
	b.Install = piInstall
	writeBundle(t, b.Bundles[1], []contract.Addon{{Kind: contract.AddonPackage, Name: "fixture-package", Source: "npm:fixture-package@9.9.9"}})
	_, problems, err := b.apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "npm:fixture-package@9.9.9") || !strings.Contains(problems[0], "install failed") || !strings.Contains(problems[0], "failed with code") {
		t.Fatalf("%q", problems)
	}
}

// A registry that does not answer costs the install its timeout, and not the box its start; the install ends with its
// context, and no npm is left running.
func TestAnInstallThatHangsEndsWithItsContext(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	registry(t, pigtest.Package(t, ""), hang)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := piInstall(ctx, t.TempDir(), fixturePackage().Source)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
