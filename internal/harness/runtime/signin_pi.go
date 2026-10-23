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
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	harnesshome "dyna-agent/internal/harness/home"
)

const piSignInCommand = "_pi-signin"
const piSignInPrefix = "PI_SIGNIN "

func piOAuthArgv(argv []string) []string {
	exe, err := os.Executable()
	if err != nil || len(argv) == 0 {
		return nil
	}
	return []string{exe, "harness", "accounts", piSignInCommand, "--pi", argv[0]}
}

func parsePiSignIn(st *SignInState, line string) {
	raw, ok := strings.CutPrefix(line, piSignInPrefix)
	if !ok || len(raw) > 64<<10 {
		return
	}
	var next SignInState
	if json.Unmarshal([]byte(raw), &next) != nil || len(next.Choices) > 100 {
		return
	}
	if next.URL != "" {
		u, err := url.Parse(next.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return
		}
	}
	for _, choice := range next.Choices {
		if choice.ID == "" || len(choice.ID) > 128 || choice.Label == "" || len(choice.Label) > 200 {
			return
		}
	}
	if message := []rune(next.Message); len(message) > signInMessageLimit {
		next.Message = string(message[:signInMessageLimit])
	}
	next.Phase = signInWaiting
	*st = next
}

// IsPiSignInCommand selects the helper before the CLI restores the owner's HOME.
func IsPiSignInCommand(args []string) bool {
	return len(args) >= 3 && args[0] == "harness" && args[1] == "accounts" && args[2] == piSignInCommand
}

func PiSignInCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(piSignInCommand, flag.ContinueOnError)
	pi := flags.String("pi", "", "Pi executable of the selected runtime bundle")
	if err := flags.Parse(args[3:]); err != nil {
		return err
	}
	scope := os.Getenv("PI_CODING_AGENT_DIR")
	if *pi == "" || flags.NArg() != 0 || !filepath.IsAbs(scope) || os.Getenv("HOME") != harnesshome.Home(scope) {
		return errors.New("Pi OAuth needs its private account home.")
	}
	return runPiOAuth(ctx, *pi, scope, os.Stdin, os.Stdout)
}

// runPiOAuth uses the SDK embedded in the selected Pi binary. The extension
// exits before Pi can create a model session or run a prompt.
func runPiOAuth(ctx context.Context, pi, scope string, in io.Reader, out io.Writer) error {
	dir, err := os.MkdirTemp(scope, ".oauth-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	extension := filepath.Join(dir, "signin.mjs")
	if err := os.WriteFile(extension, []byte(piOAuthExtension), 0600); err != nil {
		return err
	}
	argv := []string{
		"--print", "--no-session", "--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-context-files",
		"--no-mcp", "--no-tools", "--no-approve", "--extension", extension,
	}
	cmd := exec.CommandContext(ctx, pi, argv...)
	cmd.Dir = scope
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	cmd.WaitDelay = time.Second
	// Pi stays in the helper's private process group. Cancellation joins it;
	// the shared runner also kills every group in the helper's OS session.
	return childenv.Clean(cmd).Run()
}

// piOAuthExtension calls Pi's own provider login and credential store. It
// loads no user extensions, provider files, project rules, or model sessions.
const piOAuthExtension = `
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { createInterface } from "node:readline";

export default async function () {
  const rl = createInterface({input: process.stdin, terminal: false});
  const queued = [];
  const pending = [];
  let ended = false;
  let view = {url: "", code: "", message: "", choices: [], acceptsPaste: true};
  function publish(patch) {
    view = {...view, ...patch};
    console.log("PI_SIGNIN " + JSON.stringify(view));
  }
  rl.on("line", line => {
    if (pending.length) pending.shift().resolve(line.trim());
    else queued.push(line.trim());
  });
  rl.on("close", () => {
    ended = true;
    for (const entry of pending.splice(0)) entry.reject(new Error("Input closed"));
  });
  function readLine(signal) {
    if (signal?.aborted) return Promise.reject(new Error("Login cancelled"));
    if (queued.length) return Promise.resolve(queued.shift());
    if (ended) return Promise.reject(new Error("Input closed"));
    return new Promise((resolve, reject) => {
      const cleanup = () => signal?.removeEventListener("abort", abort);
      const entry = {
        resolve(value) { cleanup(); resolve(value); },
        reject(error) { cleanup(); reject(error); }
      };
      const abort = () => {
        const index = pending.indexOf(entry);
        if (index >= 0) pending.splice(index, 1);
        entry.reject(new Error("Login cancelled"));
      };
      pending.push(entry);
      signal?.addEventListener("abort", abort, {once: true});
    });
  }
  async function choose(message, options, signal) {
    publish({url: "", code: "", message, choices: options, acceptsPaste: true});
    const value = await readLine(signal);
    if (!options.some(option => option.id === value)) throw new Error("Invalid choice");
    return value;
  }
  try {
    const runtime = await ModelRuntime.create({modelsPath: null, refreshOnCreate: false});
    const providers = runtime.getProviders().filter(provider => provider.auth?.oauth)
      .map(provider => ({id: provider.id, label: provider.name ?? provider.id}));
    const provider = await choose("Choose an OAuth provider.", providers);
    await runtime.login(provider, "oauth", {
      async prompt(prompt) {
        if (prompt.type === "select") {
          // Prefer the native headless method when Pi offers it.
          if (prompt.options.some(option => option.id === "copy_code")) return "copy_code";
          return choose(prompt.message, prompt.options, prompt.signal);
        }
        publish({message: prompt.message, choices: [], acceptsPaste: true});
        return readLine(prompt.signal);
      },
      notify(event) {
        if (event.type === "auth_url") {
          publish({url: event.url, code: "", choices: [], message: event.instructions ?? "Open the sign-in link.", acceptsPaste: true});
        } else if (event.type === "device_code") {
          publish({url: event.verificationUri, code: event.userCode, choices: [], message: "Open the link and enter this code.", acceptsPaste: false});
        } else if (event.message) {
          publish({message: event.message});
        }
      }
    });
    rl.close();
    process.exit(0);
  } catch {
    rl.close();
    console.error("Pi OAuth sign-in did not finish. Try again.");
    process.exit(1);
  }
}
`
