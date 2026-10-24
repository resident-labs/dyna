package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	harnesshome "dyna-agent/internal/harness/home"
)

const (
	kimiSignInCommand = "_kimi-signin"
	kimiSignInPrefix  = "KIMI_SIGNIN "
)

var kimiDeviceCode = regexp.MustCompile(`^[A-Za-z0-9-]{4,64}$`)

func kimiSignInArgv(argv []string) []string {
	exe, err := os.Executable()
	if err != nil || len(argv) == 0 {
		return nil
	}
	return []string{exe, "harness", "accounts", kimiSignInCommand, "--kimi", argv[0]}
}

func kimiVerificationURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	switch u.Hostname() {
	case "auth.kimi.com", "www.kimi.com", "kimi.com", "auth.kimi.ai", "www.kimi.ai", "kimi.ai":
		return true
	}
	return false
}

func parseKimiSignIn(st *SignInState, line string) {
	if raw, ok := strings.CutPrefix(line, kimiSignInPrefix); ok {
		if len(raw) > 64<<10 {
			return
		}
		var next SignInState
		if json.Unmarshal([]byte(raw), &next) != nil || len(next.Choices) > 10 {
			return
		}
		if next.URL != "" && !kimiVerificationURL(next.URL) {
			return
		}
		if next.Code != "" && !kimiDeviceCode.MatchString(next.Code) {
			return
		}
		for _, choice := range next.Choices {
			if choice.ID != "mainland-cn" && choice.ID != "global" || choice.Label == "" || len(choice.Label) > 200 {
				return
			}
		}
		next.Phase = signInWaiting
		*st = next
		return
	}
	const urlPrefix = "Opening browser for Kimi device login: "
	const codePrefix = "If the browser did not open, paste the URL above and enter code: "
	switch value, ok := strings.CutPrefix(line, urlPrefix); {
	case ok:
		if !kimiVerificationURL(value) {
			return
		}
		st.URL = value
		st.Choices = nil
		st.AcceptsPaste = false
	default:
		value, ok = strings.CutPrefix(line, codePrefix)
		if !ok || !kimiDeviceCode.MatchString(value) {
			return
		}
		st.Code = value
		st.Choices = nil
		st.AcceptsPaste = false
	}
	if st.URL != "" && st.Code != "" {
		st.Phase = signInWaiting
	}
}

// IsKimiSignInCommand selects the region helper before the CLI restores HOME.
func IsKimiSignInCommand(args []string) bool {
	return len(args) >= 3 && args[0] == "harness" && args[1] == "accounts" && args[2] == kimiSignInCommand
}

// KimiSignInCommand runs region selection and native device login without
// creating a model session or reading an existing token.
func KimiSignInCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(kimiSignInCommand, flag.ContinueOnError)
	kimi := flags.String("kimi", "", "Kimi executable of the selected runtime bundle")
	if err := flags.Parse(args[3:]); err != nil {
		return err
	}
	scope := os.Getenv("KIMI_CODE_HOME")
	if *kimi == "" || flags.NArg() != 0 || !filepath.IsAbs(scope) || os.Getenv("HOME") != harnesshome.Home(scope) {
		return errors.New("Kimi Code OAuth needs its private account home")
	}
	return runKimiOAuth(ctx, *kimi, scope, os.Stdin, os.Stdout)
}

func runKimiOAuth(ctx context.Context, kimi, scope string, in io.Reader, out io.Writer) error {
	writer := bufio.NewWriter(out)
	publish := func(state SignInState) error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = writer.WriteString(kimiSignInPrefix + string(raw) + "\n"); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := publish(SignInState{
		Phase: signInWaiting, Message: "Choose a Kimi region.", AcceptsPaste: true,
		Choices: []SignInChoice{{ID: "mainland-cn", Label: "Kimi.com (mainland China)"}, {ID: "global", Label: "Kimi.ai (global)"}},
	}); err != nil {
		return err
	}
	region, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	region = strings.TrimSpace(region)
	if region != "mainland-cn" && region != "global" {
		return errors.New("Choose a valid Kimi region")
	}
	cmd := exec.CommandContext(ctx, kimi, "login", "--region", region)
	cmd.Dir = scope
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	cmd.WaitDelay = time.Second
	if err := childenv.Clean(cmd).Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Kimi Code sign-in did not finish")
	}
	return nil
}
