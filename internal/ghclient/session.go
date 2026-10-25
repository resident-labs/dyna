package ghclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
)

// ReadOnlyEnv marks the gh multicall guard, used only by read-only sessions.
const ReadOnlyEnv = "DYNA_GH_READ_ONLY"
const BinaryEnv = "DYNA_GH_BINARY"
const blockedPushURL = "file:///dev/null/Dyna GitHub writes are disabled: this room is read-only"

// SessionEnv is the environment that gives a turn the room's GitHub account,
// with the one line the agent needs when GitHub is unavailable.
type SessionEnv struct {
	Env    map[string]string
	Notice string
}

// SessionEnvironment gives plain gh the room account without storing a token.
// The token has the same trust level as credentials in the owner's shell.
// The private scratch symlink selects the executable; it stores no runtime state.
// GitHub tooling never fails the call: a rate limit, an unreachable API, a
// missing CLI, or no usable sign-in starts the turn without GitHub commands,
// and Notice says why in one line. Storage and authority errors stay fatal.
func SessionEnvironment(ctx context.Context, roomRef, scratchDir, dynaBinary string, inherited []string) (SessionEnv, error) {
	rec, err := room.DefaultStore().Resolve(roomRef)
	if err != nil {
		return SessionEnv{}, err
	}
	out := map[string]string{"GH_TOKEN": "", "GITHUB_TOKEN": "", "GH_ENTERPRISE_TOKEN": "", "GITHUB_ENTERPRISE_TOKEN": "", "GH_REPO": ""}
	cmd := Command(ctx, "", rec.Repo)
	r := repositoryFor(ctx, cmd)
	if r.name == "" {
		return SessionEnv{Env: out}, nil
	}
	ctx = WithRoom(ctx, rec.ID)
	readOnly := Access(ctx, rec) != ReadWrite
	// installed is false when gh is absent, so no account probe can succeed.
	installed := filepath.IsAbs(cmd.Path) && isExecutable(cmd.Path)
	notice, token := "", ""
	// unavailable means this turn could not prove which account it may use.
	unavailable := false
	switch {
	case !installed:
		// The turn runs without GitHub commands, and one line says so.
		notice, unavailable = missingCLINotice, true
	default:
		_, token, err = selectAccount(ctx, cmd, r)
		if err != nil {
			// A turn must run even while GitHub refuses requests, so a limit, an
			// unreachable API, or no usable sign-in becomes one line for the agent.
			var probe *accountProbeError
			switch {
			case errors.As(err, &probe) && probe.transient:
				notice, token, unavailable = probeNotice(probe), "", true
			case errors.Is(err, ErrNoSignedInAccount):
				notice, token, unavailable = noSignedInAccountNotice, "", true
			default:
				return SessionEnv{}, err
			}
		}
	}
	tokenKey, fallbackKey := "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"
	if r.host == "github.com" || strings.HasSuffix(r.host, ".ghe.com") {
		tokenKey, fallbackKey = "GH_TOKEN", "GITHUB_TOKEN"
	}
	if token == "" && !unavailable {
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
		return SessionEnv{}, fmt.Errorf("GitHub session tools require absolute scratch and binary paths")
	}
	values := map[string]string{}
	for _, entry := range inherited {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	bin := filepath.Join(scratchDir, "github-bin")
	out[ReadOnlyEnv] = ""
	target := cmd.Path
	if !installed {
		// There is no gh to point PATH at, and read-only rooms still need their guard.
		target = ""
	}
	if readOnly {
		// The guard stands in for gh, so a read-only room stays read-only.
		target, out[ReadOnlyEnv] = dynaBinary, "1"
	}
	if target != "" {
		if err := os.MkdirAll(bin, 0700); err != nil {
			return SessionEnv{}, err
		}
		next := filepath.Join(bin, "gh.next")
		_ = os.Remove(next)
		if err := os.Symlink(target, next); err != nil {
			return SessionEnv{}, err
		}
		if err := os.Rename(next, filepath.Join(bin, "gh")); err != nil {
			return SessionEnv{}, err
		}
		out[BinaryEnv] = cmd.Path
		out["PATH"] = bin + string(os.PathListSeparator) + values["PATH"]
	}
	if unavailable {
		// The turn could not prove its account, so no other account may stand in:
		// point the GitHub CLI at an empty configuration and give git no helper.
		config := filepath.Join(bin, "config")
		if err := os.MkdirAll(config, 0700); err != nil {
			return SessionEnv{}, err
		}
		out["GH_CONFIG_DIR"] = config
	}
	// Read-only rooms get no helper; its keys still clear a parent's helper.
	credentials := sessionCredentialHelper(r, cmd.Path)
	if err := sessionGitConfig(ctx, rec, readOnly, credentials, !readOnly && token != "", values, out); err != nil {
		return SessionEnv{}, err
	}
	return SessionEnv{Env: out, Notice: notice}, nil
}

// missingCLINotice is the one line a turn needs when the GitHub CLI is absent.
const missingCLINotice = "The GitHub CLI (gh) is not installed, so GitHub commands may fail this turn; continue without them."

// noSignedInAccountNotice is the one line a turn needs when no saved login can
// reach the room's repository.
const noSignedInAccountNotice = "No signed-in GitHub account can reach this repository, so GitHub commands may fail this turn; continue without them."

// probeNotice is the one line a turn needs when GitHub refused the access
// check. It names the reset time when GitHub gave one.
func probeNotice(probe *accountProbeError) string {
	if probe.until.IsZero() {
		return "GitHub is rate limited or unreachable, so GitHub commands may fail this turn; continue without them."
	}
	return "GitHub is rate limited until " + probe.until.UTC().Format(time.RFC3339) + ", so GitHub commands may fail this turn; continue without them."
}

// sessionCredentialHelper lets plain git push to the room's repository with the
// session's GH_TOKEN. The helper answers only for that repository's HTTPS URL,
// and no token or configuration is saved to disk.
func sessionCredentialHelper(r repository, ghPath string) [][2]string {
	helper := "!" + shellquote.Quote(ghPath) + " auth git-credential"
	return [][2]string{
		{"credential.https://" + r.host + ".useHttpPath", "true"},
		{"credential.https://" + r.host + "/" + r.name + ".helper", helper},
		{"credential.https://" + r.host + "/" + r.name + ".git.helper", helper},
	}
}

func sessionGitConfig(ctx context.Context, rec room.Record, readOnly bool, credentials [][2]string, grant bool, inherited, out map[string]string) error {
	count := 0
	if raw := inherited["GIT_CONFIG_COUNT"]; raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 0 || count > 1024 {
			return fmt.Errorf("invalid inherited GIT_CONFIG_COUNT")
		}
	}
	ownCredentialKeys := map[string]bool{}
	for _, entry := range credentials {
		ownCredentialKeys[entry[0]] = true
	}
	keys, values := []string{}, []string{}
	for i := 0; i < count; i++ {
		key := inherited[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]
		value := inherited[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)]
		if key == "url."+blockedPushURL+".pushInsteadOf" {
			continue
		}
		// A parent session's helper is replaced below, so it never accumulates
		// and a read-only child does not keep its parent's push credentials.
		if ownCredentialKeys[key] {
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
	if grant {
		for _, entry := range credentials {
			keys, values = append(keys, entry[0]), append(values, entry[1])
		}
	}
	for i, key := range keys {
		out[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] = key
		out[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = values[i]
	}
	out["GIT_CONFIG_COUNT"] = strconv.Itoa(len(keys))
	return nil
}
