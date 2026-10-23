package runtime

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dyna-agent/internal/account/native"
	harnesshome "dyna-agent/internal/harness/home"
)

var (
	// OpenCode includes the CLI client ID in its own device URL.
	openCodeDeviceURL  = regexp.MustCompile(`^https://opencode\.ai/console/device\?user_code=([A-Z0-9]{4}-[A-Z0-9]{4})(?:&client_id=opencode-cli)?$`)
	openCodeDeviceCode = regexp.MustCompile(`^Enter code: ([A-Z0-9]{4}-[A-Z0-9]{4})$`)
)

// Saved OpenCode Go accounts use the provider-key importer instead of OAuth.
func openCodeKeySignIn(account NativeAccount) bool {
	return account.Harness == "opencode" && strings.HasPrefix(account.Identity, "opencode:opencode-go:")
}

func openCodeDeviceArgv(argv []string) []string {
	argv = append([]string(nil), argv...)
	// The account manager leaves the target unset. Choose OpenCode Zen,
	// which supports device sign-in without a terminal.
	if len(argv) == 3 {
		argv = append(argv, "opencode")
	}
	return append(argv, "--method", "device")
}

func parseOpenCodeSignIn(st *SignInState, line string) {
	// OpenCode's CLI adds a bullet even when its output is a pipe.
	line = strings.TrimPrefix(line, "● ")
	if match := openCodeDeviceURL.FindStringSubmatch(line); match != nil {
		st.URL = line
		st.Code = match[1]
	} else if match := openCodeDeviceCode.FindStringSubmatch(line); match != nil {
		st.Code = match[1]
	} else {
		return
	}
	if st.URL != "" && st.Code != "" {
		st.Phase = signInWaiting
	}
}

const openCodeGoSignInCommand = "_opencode-go-signin"
const openCodeGoKeyPrompt = "OpenCode Go API key:"

var openCodeGoSignInSpec = signInSpec{
	Argv: openCodeGoArgv, AcceptsPaste: true, GracefulStop: true,
	Parse: func(st *SignInState, line string) {
		if line == openCodeGoKeyPrompt {
			st.Phase = signInWaiting
			st.SecretInput = true
			st.Message = "Paste your OpenCode Go API key."
		}
	},
}

func openCodeGoArgv(argv []string) []string {
	exe, err := os.Executable()
	if err != nil || len(argv) == 0 {
		return nil
	}
	return []string{exe, "harness", "accounts", openCodeGoSignInCommand, "--opencode", argv[0]}
}

// IsOpenCodeGoSignInCommand selects the helper before the owner's HOME is restored.
func IsOpenCodeGoSignInCommand(args []string) bool {
	return len(args) >= 3 && args[0] == "harness" && args[1] == "accounts" && args[2] == openCodeGoSignInCommand
}

func OpenCodeGoSignInCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(openCodeGoSignInCommand, flag.ContinueOnError)
	exe := flags.String("opencode", "", "OpenCode executable of the selected runtime bundle")
	if err := flags.Parse(args[3:]); err != nil {
		return err
	}
	scope := os.Getenv("OPENCODE_CONFIG_DIR")
	if *exe == "" || flags.NArg() != 0 || !filepath.IsAbs(scope) || os.Getenv("HOME") != harnesshome.Home(scope) || os.Getenv("OPENCODE_DB") != filepath.Join(scope, "opencode.db") {
		return errors.New("OpenCode Go sign-in needs its private account home.")
	}
	host := native.Host{OpenCodeService: true, Executable: *exe, Home: os.Getenv("HOME"), Env: os.Getenv}
	return runOpenCodeGoSignIn(ctx, host, os.Stdin, os.Stdout)
}

// runOpenCodeGoSignIn uses the provider-key importer. Native login requires a
// terminal for key entry, while auth import takes the key through stdin.
func runOpenCodeGoSignIn(ctx context.Context, host native.Host, in io.Reader, out io.Writer) error {
	manager, _ := native.For("opencode", host)
	keys, _ := native.OpenCodeKeysOf(manager)
	if _, err := fmt.Fprintln(out, openCodeGoKeyPrompt); err != nil {
		return err
	}
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 1024), maxProviderKey+2)
	if !lines.Scan() {
		return errors.New("OpenCode Go sign-in needs an API key.")
	}
	key := strings.TrimSpace(lines.Text())
	if key == "" || len(key) > maxProviderKey || strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("The API key must fit on one line.")
	}
	if err := keys.Add(ctx, "opencode-go", key); err != nil {
		return errors.New("OpenCode Go could not save the API key. Try again.")
	}
	return nil
}
