package roommcp

import (
	"bytes"
	"context"
	"encoding/json"
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

func (h Host) run(ctx context.Context, dir, name string, args ...string) string {
	if h.Run != nil {
		out, _ := h.Run(ctx, dir, name, args...)
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, commandLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	// A missing key or a missing tool leaves the answer empty.
	_ = cmd.Run()
	return out.String()
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
			if value := strings.TrimSpace(h.run(ctx, repo.Path, "git", "config", "--get", key)); value != "" {
				ids.Personal = append(ids.Personal, value)
			}
		}
		for _, match := range remoteOwner.FindAllStringSubmatch(h.run(ctx, repo.Path, "git", "config", "--get-regexp", `^remote\..*\.url$`), -1) {
			ids.Logins = append(ids.Logins, match[1])
		}
	}
	var status struct {
		Hosts map[string][]struct {
			Login string `json:"login"`
		} `json:"hosts"`
	}
	if json.Unmarshal([]byte(h.run(ctx, "", "gh", "auth", "status", "--json", "hosts")), &status) == nil {
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
