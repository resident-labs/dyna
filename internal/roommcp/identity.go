package roommcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/room"
)

// IdentitySource reads what the argument scanner must keep out of a call made
// for one room.
type IdentitySource func(ctx context.Context, rec room.Record) (Identities, error)

const commandLimit = 10 * time.Second

var (
	// remoteOwner reads the account or organization from a GitHub remote URL.
	remoteOwner = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9][A-Za-z0-9-]*)/`)
	// secretKey names the credential fields that hold secret values.
	secretKey = regexp.MustCompile(`(?i)token|secret|key|password|cookie|credential|bearer|auth`)
)

// Host reads identities from this machine: the git identity of the room's
// repositories, the saved accounts, and the GitHub CLI's logins.
type Host struct {
	Accounts *accountstore.Store
	// Home is the owner's home directory.
	Home string
	// Run runs a command in dir and returns its standard output. A nil Run
	// starts the real command.
	Run func(ctx context.Context, dir, name string, args ...string) (string, error)
}

// run runs a command and returns its standard output. A command that exits
// with status 1 has nothing to report only when it is an unset git key or a
// GitHub CLI that answered with JSON. Any other failure is an error: the
// scanner must not treat an unfinished lookup as an empty answer.
func (h Host) run(ctx context.Context, dir, name string, args ...string) (string, error) {
	var out string
	var err error
	if h.Run != nil {
		out, err = h.Run(ctx, dir, name, args...)
	} else {
		ctx, cancel := context.WithTimeout(ctx, commandLimit)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		var buf bytes.Buffer
		cmd.Stdout = &buf
		err = cmd.Run()
		out = buf.String()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1 && (name == "git" || json.Valid([]byte(out))):
		// git exits 1 for an unset key. The GitHub CLI exits 1 for many
		// failures, so only a JSON answer confirms a signed-out state.
		return out, nil
	case name == "gh" && errors.Is(err, exec.ErrNotFound):
		return "", nil
	}
	return "", fmt.Errorf("%s: %w", name, err)
}

// Identities implements IdentitySource.
func (h Host) Identities(ctx context.Context, rec room.Record) (Identities, error) {
	ids := Identities{Home: h.Home}
	st, err := h.Accounts.Read()
	if err != nil {
		return Identities{}, err
	}
	for _, entry := range st.Entries {
		if entry.Identity != "" {
			ids.Personal = append(ids.Personal, entry.Identity)
		}
		// A credential payload has an adapter-owned shape, so walk it as plain JSON.
		var payload any
		if json.Unmarshal(entry.Credential.Payload, &payload) == nil {
			ids.Secrets = collectSecrets(ids.Secrets, "", payload)
		}
	}
	for _, repo := range rec.Repos() {
		if repo.Missing {
			continue
		}
		for _, key := range []string{"user.name", "user.email"} {
			value, err := h.run(ctx, repo.Path, "git", "config", "--get", key)
			if err != nil {
				return Identities{}, err
			}
			if value = strings.TrimSpace(value); value != "" {
				ids.Personal = append(ids.Personal, value)
			}
		}
		remotes, err := h.run(ctx, repo.Path, "git", "config", "--get-regexp", `^remote\..*\.url$`)
		if err != nil {
			return Identities{}, err
		}
		for _, match := range remoteOwner.FindAllStringSubmatch(remotes, -1) {
			ids.Logins = append(ids.Logins, match[1])
		}
	}
	var status struct {
		Hosts map[string][]struct {
			Login string `json:"login"`
		} `json:"hosts"`
	}
	hosts, err := h.run(ctx, "", "gh", "auth", "status", "--json", "hosts")
	if err != nil {
		return Identities{}, err
	}
	if hosts != "" {
		if err := json.Unmarshal([]byte(hosts), &status); err != nil {
			return Identities{}, fmt.Errorf("read GitHub logins: %w", err)
		}
		for _, accounts := range status.Hosts {
			for _, account := range accounts {
				ids.Logins = append(ids.Logins, account.Login)
			}
		}
	}
	return ids, nil
}

// collectSecrets adds the string values under secret-looking keys of a
// credential. A value this short is not a secret worth matching.
func collectSecrets(out []string, key string, value any) []string {
	switch v := value.(type) {
	case string:
		if len(v) >= 12 && secretKey.MatchString(key) {
			out = append(out, v)
		}
	case map[string]any:
		for k, child := range v {
			if secretKey.MatchString(key) {
				k = key
			}
			out = collectSecrets(out, k, child)
		}
	case []any:
		for _, child := range v {
			out = collectSecrets(out, key, child)
		}
	}
	return out
}
