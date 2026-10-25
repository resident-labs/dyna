package runtime

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/mode"
)

// piRenewScript asks Pi to renew one saved OAuth grant. Pi resolves the
// provider's own refresh hook and takes its auth.json lock while it renews, so
// Dyna keeps no provider refresh protocol of its own and never races Pi's
// writer.
//
//go:embed pi_renew.mjs
var piRenewScript string

// piRenewRunner runs the renewal script for one account home and provider. It
// returns nil when the grant is usable, native.ErrInvalidGrant when the provider
// rejected it, and another error when the run itself failed. Tests replace it.
var piRenewRunner = runPiRenewScript

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
	if err = piRenewRunner(ctx, home, provider, m.host.Executable); err != nil {
		return c, err
	}
	entries, err := readPiAuthEntries(filepath.Join(home, "auth.json"))
	if err != nil {
		return c, err
	}
	renewed, err := native.PiCapturedFromAuth(entries)
	if err != nil {
		return c, err
	}
	if renewed.Identity != c.Identity {
		return c, native.ErrNoIdentity
	}
	return renewed, nil
}

// runPiRenewScript runs the embedded script with the Pi bundle's own package and
// node.
func runPiRenewScript(ctx context.Context, home, provider, executable string) error {
	pkg, err := piPackageDir(executable)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dyna-pi-renew-*")
	if err != nil {
		return errors.New("the Pi renewal script could not be prepared")
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "pi_renew.mjs")
	if err = os.WriteFile(script, []byte(piRenewScript), 0600); err != nil {
		return errors.New("the Pi renewal script could not be prepared")
	}
	cmd := exec.CommandContext(ctx, "node", script, filepath.Join(home, "auth.json"), provider)
	cmd.Dir = dir
	cmd.Env = append(envWithout(os.Environ(), mode.EnvSession, mode.EnvMode, mode.EnvRoom, mode.EnvDepth),
		"PI_CODING_AGENT_DIR="+home,
		"DYNA_PI_PACKAGE="+pkg,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := strings.TrimSpace(stderr.String())
	if line, _, _ := strings.Cut(out, "\n"); strings.HasPrefix(line, "error ") {
		return classifyPiRenewFailure(line)
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && exit.ExitCode() == 3 {
			return native.ErrNotSignedIn
		}
		return errors.New("Pi could not renew this sign-in")
	}
	return nil
}

// classifyPiRenewFailure decides whether a renewal failure is a rejected grant
// or a transient one. Only the provider's own signal counts: an authentication
// status or the invalid_grant code. Pi wraps every refresh failure in the same
// message, so its wording is never used, and a network error, a timeout, a 429
// or a 5xx stays transient and never asks the owner to sign in again.
func classifyPiRenewFailure(line string) error {
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(fields[0], "error") {
		return errors.New("Pi could not renew this sign-in")
	}
	status, _ := strconv.Atoi(fields[1])
	switch fields[2] {
	case "invalid_grant":
		return native.ErrInvalidGrant
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest:
		return native.ErrInvalidGrant
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

// piPackageDir resolves the Pi package a bundle executable belongs to.
func piPackageDir(executable string) (string, error) {
	if executable == "" {
		return "", errors.New("Dyna's Pi copy is not installed")
	}
	prefix := filepath.Dir(filepath.Dir(executable))
	dir := filepath.Join(prefix, "lib", "node_modules", "@earendil-works", "pi-coding-agent")
	if info, err := os.Stat(filepath.Join(dir, "dist", "index.js")); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("Dyna's Pi copy has no provider runtime")
	}
	return dir, nil
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
