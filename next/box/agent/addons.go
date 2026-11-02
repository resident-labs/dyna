package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

// installTimeout bounds what one box start spends on packages it has not cached, so a hung registry costs a notice and not the box.
const installTimeout = 3 * time.Minute

// addonStart feeds a scope's add-ons to Pi the way Pi takes them: skills in the folder it discovers, packages in its
// settings, and servers in its MCP config. It runs before Pi at every box start, so a new item waits for the next one.
type addonStart struct {
	Bundles  []string // global, then room: on a clash the later scope wins
	Skills   string   // Pi's skills folder, which this rebuilds
	Settings string   // Pi's settings file
	Cache    string   // one install per source, shared by the host's boxes
	Install  func(ctx context.Context, dir, source string) error
}

func roomAddons() addonStart {
	return addonStart{
		Bundles:  []string{"/run/dyna/instructions/global/" + box.AddonBundle, "/run/dyna/instructions/room/" + box.AddonBundle},
		Skills:   "/box/pi/skills",
		Settings: "/box/pi/settings.json",
		Cache:    "/box/cache/pi-packages",
		Install:  piInstall,
	}
}

// chosenAddon is one item with the folder its bundle unpacked to.
type chosenAddon struct {
	contract.Addon
	dir string
}

// apply returns the added MCP servers and, in plain words, each item that failed. Only a box that cannot write its own
// folders gets an error.
func (a addonStart) apply(ctx context.Context) (map[string]pi.MCPServer, []string, error) {
	work, err := os.MkdirTemp(filepath.Dir(a.Skills), ".addons-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(work)
	var problems []string
	chosen := map[string]chosenAddon{}
	for i, path := range a.Bundles {
		dir := filepath.Join(work, strconv.Itoa(i))
		items, err := readBundle(path, dir)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
		}
		for _, item := range items {
			chosen[string(item.Kind)+" "+item.Name] = chosenAddon{item, dir}
		}
	}
	if err := os.RemoveAll(a.Skills); err != nil {
		return nil, nil, err
	}
	if err := skills.Install(a.Skills); err != nil {
		return nil, nil, err
	}
	servers := map[string]pi.MCPServer{}
	var packages []contract.Addon
	for _, key := range slices.Sorted(maps.Keys(chosen)) {
		item := chosen[key]
		switch item.Kind {
		case contract.AddonSkill:
			if err := os.Rename(filepath.Join(item.dir, item.Name), filepath.Join(a.Skills, item.Name)); err != nil {
				problems = append(problems, fmt.Sprintf("skill %s: %v", item.Name, err))
			}
		case contract.AddonPackage:
			packages = append(packages, item.Addon)
		case contract.AddonMCP:
			servers[item.Name] = pi.MCPServer{URL: item.URL, Command: item.Command, Args: item.Args, Exposure: "codemode"}
		}
	}
	paths, failed := a.install(ctx, packages)
	// Pi asks before it trusts a repository's own resources, and a box has nobody to ask, so repository skills need this.
	settings, err := json.Marshal(struct {
		DefaultProjectTrust string   `json:"defaultProjectTrust"`
		Packages            []string `json:"packages,omitempty"`
	}{"always", paths})
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(a.Settings, settings, 0o600); err != nil {
		return nil, nil, err
	}
	return servers, append(problems, failed...), nil
}

func readBundle(path, dir string) ([]contract.Addon, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := skills.Extract(data, dir); err != nil {
		return nil, err
	}
	list, err := os.ReadFile(filepath.Join(dir, skills.ListFile))
	if err != nil {
		return nil, err
	}
	var parsed skills.List
	return parsed.Items, json.Unmarshal(list, &parsed)
}

// install makes every package ready at once, so that one that hangs does not hold up the rest. It returns the paths of
// those that worked, in order, for Pi to load as local packages, and leaves the rest out.
func (a addonStart) install(ctx context.Context, packages []contract.Addon) ([]string, []string) {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	paths, failures := make([]string, len(packages)), make([]error, len(packages))
	var running sync.WaitGroup
	for i, item := range packages {
		running.Go(func() { paths[i], failures[i] = a.installOne(ctx, item) })
	}
	running.Wait()
	var problems []string
	for i, err := range failures {
		if err != nil {
			problems = append(problems, fmt.Sprintf("package %s: %v", packages[i].Source, err))
		}
	}
	return slices.DeleteFunc(paths, func(path string) bool { return path == "" }), problems
}

// installOne installs one pinned source at most once per host. Each source has its own folder, so two rooms can pin one package
// to different versions. Boxes that race each install to a temporary folder, and the first rename wins.
func (a addonStart) installOne(ctx context.Context, item contract.Addon) (string, error) {
	sum := sha256.Sum256([]byte(item.Source))
	folder := filepath.Join(a.Cache, hex.EncodeToString(sum[:16]))
	if _, err := os.Stat(folder); err == nil {
		return installedPath(folder, item), nil
	}
	if err := os.MkdirAll(a.Cache, 0o700); err != nil {
		return "", err
	}
	unpacked, err := os.MkdirTemp(a.Cache, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(unpacked)
	if err := a.Install(ctx, unpacked, item.Source); err != nil {
		return "", err
	}
	if _, err := os.Stat(installedPath(unpacked, item)); err != nil {
		return "", errors.New("Pi installed it somewhere else")
	}
	if err := os.Rename(unpacked, folder); err != nil {
		if _, won := os.Stat(folder); won != nil {
			return "", err
		}
	}
	return installedPath(folder, item), nil
}

// installedPath is where Pi puts a source under its agent folder.
func installedPath(root string, item contract.Addon) string {
	if strings.HasPrefix(item.Source, "npm:") {
		return root + "/npm/node_modules/" + item.Name
	}
	return root + "/git/" + item.Name
}

// piInstall has Pi install a source into its own agent folder, then starts Pi there once. Pi exits when an extension fails
// to load, so this is where a broken package must show, or it would stop every box that loads it.
func piInstall(ctx context.Context, dir, source string) error {
	pi := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "pi", args...)
		cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+dir)
		cmd.WaitDelay = 5 * time.Second // npm can outlive pi and hold its output open
		return cmd.CombinedOutput()
	}
	out, err := pi("install", source)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("install failed: %s", lastOutput(out))
	}
	// Without a model Pi exits right after loading, so only this message says anything.
	out, _ = pi("--offline", "--mode", "rpc", "--no-session")
	if _, failure, found := bytes.Cut(out, []byte("Failed to load extension")); found {
		line, _, _ := bytes.Cut(failure, []byte("\n"))
		return fmt.Errorf("its extension failed to load:%s", line)
	}
	return nil
}

func lastOutput(out []byte) string {
	text := strings.ToValidUTF8(strings.TrimSpace(string(out)), "")
	if len(text) > 400 {
		text = "…" + strings.ToValidUTF8(text[len(text)-400:], "")
	}
	return text
}

// addonNotice is the plain notice for what failed, or "" when nothing did.
func addonNotice(problems []string) string {
	if len(problems) == 0 {
		return ""
	}
	return "These add-ons did not load at this box start:\n" + strings.Join(problems, "\n")
}
