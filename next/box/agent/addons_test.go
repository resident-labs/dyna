package agent

import (
	"context"
	"encoding/json"
	"errors"
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
		Packages            []string `json:"packages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
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
	if servers["docs"].URL != "https://room.test/mcp" || servers["docs"].Exposure != "codemode" || servers["only-global"].Command != "server" {
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
