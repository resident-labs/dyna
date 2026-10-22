package ghclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

// ReadOnlyEnv marks the gh multicall guard, used only by read-only sessions.
const ReadOnlyEnv = "DYNA_GH_READ_ONLY"
const BinaryEnv = "DYNA_GH_BINARY"
const blockedPushURL = "file:///dev/null/Dyna GitHub writes are disabled: this room is read-only"

// SessionEnvironment gives plain gh the room account without storing a token.
// The token has the same trust level as credentials in the owner's shell.
// The private scratch symlink selects the executable; it stores no runtime state.
func SessionEnvironment(ctx context.Context, roomRef, scratchDir, dynaBinary string, inherited []string) (map[string]string, error) {
	rec, err := room.DefaultStore().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	out := map[string]string{"GH_TOKEN": "", "GITHUB_TOKEN": "", "GH_ENTERPRISE_TOKEN": "", "GITHUB_ENTERPRISE_TOKEN": "", "GH_REPO": ""}
	cmd := Command(ctx, "", rec.Repo)
	r := repositoryFor(ctx, cmd)
	if r.name == "" {
		return out, nil
	}
	ctx = WithRoom(ctx, rec.ID)
	_, token, err := selectAccount(ctx, cmd, r)
	if err != nil {
		return nil, err
	}
	tokenKey, fallbackKey := "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"
	if r.host == "github.com" || strings.HasSuffix(r.host, ".ghe.com") {
		tokenKey, fallbackKey = "GH_TOKEN", "GITHUB_TOKEN"
	}
	if token == "" {
		// Token-only owner environments remain usable when gh has no saved accounts.
		source := map[string]string{}
		for _, entry := range inherited {
			key, value, _ := strings.Cut(entry, "=")
			source[key] = value
		}
		token = source[tokenKey]
		if token == "" {
			token = source[fallbackKey]
		}
	}
	out[tokenKey], out["GH_HOST"] = token, r.host
	if !filepath.IsAbs(scratchDir) || !filepath.IsAbs(dynaBinary) {
		return nil, fmt.Errorf("GitHub session tools require absolute scratch and binary paths")
	}
	values := map[string]string{}
	for _, entry := range inherited {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	bin := filepath.Join(scratchDir, "github-bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		return nil, err
	}
	readOnly := Access(ctx, rec) != ReadWrite
	target := cmd.Path
	if !filepath.IsAbs(target) || !isExecutable(target) {
		return nil, fmt.Errorf("the GitHub CLI (gh) is not installed")
	}
	out[ReadOnlyEnv] = ""
	if readOnly {
		target = dynaBinary
		out[ReadOnlyEnv] = "1"
	}
	next := filepath.Join(bin, "gh.next")
	_ = os.Remove(next)
	if err := os.Symlink(target, next); err != nil {
		return nil, err
	}
	if err := os.Rename(next, filepath.Join(bin, "gh")); err != nil {
		return nil, err
	}
	out[BinaryEnv] = cmd.Path
	out["PATH"] = bin + string(os.PathListSeparator) + values["PATH"]
	if err := sessionGitConfig(ctx, rec, readOnly, values, out); err != nil {
		return nil, err
	}
	return out, nil
}

func sessionGitConfig(ctx context.Context, rec room.Record, readOnly bool, inherited, out map[string]string) error {
	count := 0
	if raw := inherited["GIT_CONFIG_COUNT"]; raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 0 || count > 1024 {
			return fmt.Errorf("invalid inherited GIT_CONFIG_COUNT")
		}
	}
	keys, values := []string{}, []string{}
	for i := 0; i < count; i++ {
		key := inherited[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]
		value := inherited[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)]
		if key == "url."+blockedPushURL+".pushInsteadOf" {
			continue
		}
		if strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".pushurl") {
			if value == blockedPushURL {
				continue
			}
			if value == "" && inherited[fmt.Sprintf("GIT_CONFIG_KEY_%d", i+1)] == key && inherited[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i+1)] == blockedPushURL {
				i++
				continue
			}
		}
		keys, values = append(keys, key), append(values, value)
	}
	if readOnly {
		remotes, err := repoid.Git(ctx, rec.Repo, "remote")
		if err != nil {
			return err
		}
		for _, name := range strings.Fields(remotes) {
			// An empty pushurl clears prior values; the next value blocks this remote.
			key := "remote." + name + ".pushurl"
			keys, values = append(keys, key, key), append(values, "", blockedPushURL)
			for _, args := range [][]string{{"remote", "get-url", "--all", name}, {"remote", "get-url", "--push", "--all", name}} {
				urls, err := repoid.Git(ctx, rec.Repo, args...)
				if err != nil {
					return err
				}
				for _, remote := range strings.Split(urls, "\n") {
					keys, values = append(keys, "url."+blockedPushURL+".pushInsteadOf"), append(values, remote)
				}
			}
		}
	}
	for i, key := range keys {
		out[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] = key
		out[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = values[i]
	}
	out["GIT_CONFIG_COUNT"] = strconv.Itoa(len(keys))
	return nil
}
