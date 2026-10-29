package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"dyna-agent/internal/childenv"
	harnesshome "dyna-agent/internal/harness/home"
)

const piSignInCommand = "_pi-signin"

type piSignInEvent struct {
	Type            string         `json:"type"`
	URL             string         `json:"url"`
	Instructions    string         `json:"instructions"`
	VerificationURI string         `json:"verificationUri"`
	UserCode        string         `json:"userCode"`
	Message         string         `json:"message"`
	Options         []SignInChoice `json:"options"`
}

func piOAuthArgv(piExe, providerID string) []string {
	exe, err := os.Executable()
	if err != nil || piExe == "" {
		return nil
	}
	argv := []string{exe, "harness", "accounts", piSignInCommand, "--pi", piExe}
	if providerID != "" {
		argv = append(argv, "--provider", providerID)
	}
	return argv
}

func validPiSignInURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

// parsePiSignIn decodes the native Pi JSONL events into the shared card state.
func parsePiSignIn(st *SignInState, line string) {
	if len(line) > 64<<10 {
		return
	}
	var event piSignInEvent
	if json.Unmarshal([]byte(line), &event) != nil {
		return
	}
	next := *st
	next.Phase = signInWaiting
	switch event.Type {
	case "auth_url":
		if !validPiSignInURL(event.URL) {
			return
		}
		next.URL, next.Code, next.Choices, next.AcceptsPaste = event.URL, "", nil, true
		next.Message = event.Instructions
		if next.Message == "" {
			next.Message = "Open the sign-in link."
		}
	case "device_code":
		if !validPiSignInURL(event.VerificationURI) {
			return
		}
		next.URL, next.Code, next.Choices, next.AcceptsPaste = event.VerificationURI, event.UserCode, nil, false
		next.Message = "Open the link and enter this code."
	case "select":
		if len(event.Options) > 100 {
			return
		}
		for _, choice := range event.Options {
			if choice.ID == "" || len(choice.ID) > 128 || choice.Label == "" || len(choice.Label) > 200 {
				return
			}
		}
		next.URL, next.Code, next.AcceptsPaste = "", "", true
		next.Message, next.Choices = event.Message, event.Options
	case "prompt":
		next.Message, next.Choices, next.AcceptsPaste = event.Message, nil, true
	case "progress":
		next.Message = event.Message
	case "done":
		next.Phase, next.Choices, next.AcceptsPaste = signInVerifying, nil, false
	case "error":
		next.Message, next.Choices, next.AcceptsPaste = event.Message, nil, false
	default:
		return
	}
	if message := []rune(next.Message); len(message) > signInMessageLimit {
		next.Message = string(message[:signInMessageLimit])
	}
	*st = next
}

// IsPiSignInCommand selects the helper before the CLI restores the owner's HOME.
func IsPiSignInCommand(args []string) bool {
	return len(args) >= 3 && args[0] == "harness" && args[1] == "accounts" && args[2] == piSignInCommand
}

func PiSignInCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(piSignInCommand, flag.ContinueOnError)
	pi := flags.String("pi", "", "Pi executable of the selected runtime bundle")
	provider := flags.String("provider", "", "Pi OAuth provider ID")
	if err := flags.Parse(args[3:]); err != nil {
		return err
	}
	scope := os.Getenv("PI_CODING_AGENT_DIR")
	if *pi == "" || flags.NArg() != 0 || !filepath.IsAbs(scope) || os.Getenv("HOME") != harnesshome.Home(scope) {
		return errors.New("Pi OAuth needs its private account home.")
	}
	return runPiOAuth(ctx, *pi, scope, *provider, os.Stdin, os.Stdout)
}

// runPiOAuth asks the selected Pi fork to run its own headless OAuth flow.
func runPiOAuth(ctx context.Context, pi, scope, provider string, in io.Reader, out io.Writer) error {
	argv := []string{"auth", "login"}
	if provider != "" {
		argv = append(argv, "--provider", provider)
	}
	argv = append(argv, "--json")
	cmd := exec.CommandContext(ctx, pi, argv...)
	cmd.Dir = scope
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	cmd.WaitDelay = time.Second
	// Pi stays in the helper's private process group. Cancellation joins it;
	// the shared runner also kills every group in the helper's OS session.
	return childenv.Clean(cmd).Run()
}
