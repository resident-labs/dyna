package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/mode"
)

// piRenewRunner asks Pi to renew one account home's provider grant. It returns
// nil when the grant is usable, native.ErrInvalidGrant when the provider
// rejected it, and another error when the renewal is unavailable or transient.
var piRenewRunner = runPiRenew

// renewPi asks Pi to renew the account's grant and returns the renewed login.
func (m *nativeFilesManager) renewPi(ctx context.Context, c native.Captured) (native.Captured, error) {
	home, err := m.root("")
	if err != nil {
		return c, err
	}
	provider, _, _, _, err := native.PiGrant(c.Credential)
	if err != nil {
		return c, err
	}
	runnerErr := piRenewRunner(ctx, home, provider, m.host.Executable)
	if runnerErr != nil && !errors.Is(runnerErr, native.ErrNotSignedIn) {
		return c, runnerErr
	}
	entries, err := readPiAuthEntries(filepath.Join(home, "auth.json"))
	if err != nil {
		if runnerErr != nil {
			return c, runnerErr
		}
		return c, err
	}
	renewed, err := native.PiCapturedFromAuth(entries)
	if err != nil {
		if runnerErr != nil {
			return c, runnerErr
		}
		return c, err
	}
	if renewed.Identity != c.Identity {
		return c, native.ErrNoIdentity
	}
	// Pi's refresh command reports no_credential for an OAuth login it still
	// holds, because it checks auth.apiKey, which no OAuth provider sets. The
	// account's own auth.json is the truth: the login is there, and Pi may
	// even have rotated it while reporting the miss.
	return renewed, nil
}

type piRenewResult struct {
	Status     string `json:"status"`
	Kind       string `json:"kind"`
	HTTPStatus int    `json:"httpStatus"`
}

// runPiRenew uses the selected Pi fork's native refresh command. Pi resolves
// the provider and renews under its own auth.json lock.
func runPiRenew(ctx context.Context, home, provider, executable string) error {
	if executable == "" {
		return errors.New("Dyna's Pi copy is not installed")
	}
	cmd := exec.CommandContext(ctx, executable, "auth", "refresh", "--provider", provider, "--json")
	cmd.Dir = home
	cmd.Env = append(envWithout(os.Environ(), mode.EnvSession, mode.EnvMode, mode.EnvRoom, mode.EnvDepth),
		"PI_CODING_AGENT_DIR="+home,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	runErr := cmd.Run()
	err := classifyPiRenewResult(stdout.Bytes())
	if err == nil && runErr != nil {
		return errors.New("Pi could not renew this sign-in")
	}
	return err
}

// classifyPiRenewResult maps Pi's typed status onto the existing account
// errors. Every failure except an explicitly rejected grant stays transient.
func classifyPiRenewResult(output []byte) error {
	var result piRenewResult
	if len(output) > 64<<10 || json.Unmarshal(output, &result) != nil {
		return errors.New("Pi could not renew this sign-in")
	}
	switch result.Status {
	case "renewed":
		return nil
	case "no_credential":
		return native.ErrNotSignedIn
	case "failed":
		if result.Kind == "invalid_grant" {
			return native.ErrInvalidGrant
		}
	}
	return errors.New("Pi could not renew this sign-in")
}

// readPiAuthEntries reads one account's auth.json entry map.
func readPiAuthEntries(path string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries map[string]json.RawMessage
	if len(raw) > 4<<20 || json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, errors.New("Pi's login file is not valid JSON.")
	}
	return entries, nil
}

// envWithout drops the named variables from a copy of env.
func envWithout(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, pair := range env {
		key, _, _ := strings.Cut(pair, "=")
		drop := false
		for _, name := range names {
			if key == name {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, pair)
		}
	}
	return out
}
