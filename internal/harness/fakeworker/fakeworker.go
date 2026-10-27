// Package fakeworker lets tests run a shell script as a real harness CLI. It is
// for tests only: it imports testing, so production code must never import it.
package fakeworker

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
)

// bodyEnv carries the shell body of the turn to the fake CLI.
const bodyEnv = "DYNA_FAKE_WORKER_BODY"

// InstallClaude puts a `claude` on PATH that speaks claude-stream-json: it
// replays the user message, runs body as a shell script with DYNA_FAKE_PROMPT
// set to that message, and reports the script's stdout as the turn result. The
// script sets the exit status, and its stderr reaches Dyna unchanged.
func InstallClaude(t testing.TB, body string) profile.Profile {
	t.Helper()
	// A hosted worker keeps a session host and its child alive for a while
	// after a turn. Shut them down when the test ends, so a long test binary
	// never holds a process per worker it ever ran.
	t.Cleanup(sessionhost.ResetForTest)
	dir := t.TempDir()
	if err := fsutil.WriteExecutable(filepath.Join(dir, "claude"), []byte(claudeWrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return profile.Profile{
		Name: "fixture", Harness: profile.HarnessClaudeCode, Taste: 5, Intelligence: 5, Cost: 5,
		Env: map[string]string{
			"PATH":  dir + string(os.PathListSeparator) + os.Getenv("PATH"),
			bodyEnv: body,
		},
	}
}

// MarkSilent makes each turn take the prompt without acknowledging it, like a
// worker that hung before it read any input.
func MarkSilent(p profile.Profile) profile.Profile {
	p.Env[silentEnv] = "1"
	return p
}

// MarkStatus makes every failed turn of p report the provider's own failure
// status, such as 429 for a usage limit or 503 for a busy provider.
func MarkStatus(p profile.Profile, status int) profile.Profile {
	p.Env[statusEnv] = strconv.Itoa(status)
	return p
}

// InstallClaudeLimit installs a claude whose turns end on a provider usage
// limit, like an account with no quota left, and whose result carries message.
func InstallClaudeLimit(t testing.TB, message string) profile.Profile {
	p := MarkStatus(InstallClaude(t, "printf '%s' \"$DYNA_FAKE_LIMIT\"; exit 1"), 429)
	p.Env[messageEnv] = message
	return p
}

const (
	silentEnv  = "DYNA_FAKE_SILENT"
	statusEnv  = "DYNA_FAKE_STATUS"
	messageEnv = "DYNA_FAKE_LIMIT"
)

// claudeWrapper wraps a shell worker in Claude's persistent-input protocol.
// The worker sees the plain prompt text in the environment and closed stdin.
// The wrapper acknowledges live input and keeps the terminal result current
// until Dyna retires it.
const claudeWrapper = `#!/usr/bin/env python3
import json, os, re, select, subprocess, sys, threading
if "--version" in sys.argv:
    print("fake-worker-1.0")
    sys.exit(0)

model = ""
args = sys.argv[1:]
for i, arg in enumerate(args):
    if arg == "--model" and i + 1 < len(args):
        model = args[i + 1]
    elif arg.startswith("--model="):
        model = arg.split("=", 1)[1]

body = os.environ.get("DYNA_FAKE_WORKER_BODY", "")

def emit(value):
    print(json.dumps(value), flush=True)

# The session host closes stdin to ask for an orderly exit. A body that is
# blocked on a pipe never sees that close, so this watcher ends the process
# when the write end goes away. Production shutdown then does not wait its
# stop grace for a fixture that ignores stdin.
body_proc = {"p": None}

def stop_when_stdin_closes():
    poller = select.poll()
    try:
        poller.register(sys.stdin.fileno(), select.POLLIN)
    except (ValueError, OSError):
        return
    while True:
        try:
            events = poller.poll()
        except Exception:
            return
        for _, event in events:
            if event & (select.POLLHUP | select.POLLERR | select.POLLNVAL):
                proc = body_proc["p"]
                if proc is not None and proc.poll() is None:
                    try:
                        proc.kill()
                        proc.wait()
                    except OSError:
                        pass
                os._exit(0)
            if event & select.POLLIN:
                # Buffered input still belongs to the turn loop. Wait only for
                # hangup from here, so a live control message is not a shutdown.
                try:
                    poller.modify(sys.stdin.fileno(), 0)
                except Exception:
                    return

threading.Thread(target=stop_when_stdin_closes, daemon=True).start()

code = 0
for line in sys.stdin:
    if not line.strip():
        continue
    try:
        record = json.loads(line)
    except ValueError:
        continue
    if record.get("type") == "control_request":
        emit({"type": "control_response", "response": {
            "subtype": "success", "request_id": record.get("request_id"), "response": {}}})
        continue
    if record.get("type") != "user":
        continue
    if os.environ.get("DYNA_FAKE_SILENT") != "1":
        record["isReplay"] = True
        emit(record)
    content = record.get("message", {}).get("content")
    text = content if isinstance(content, str) else "".join(part.get("text", "") for part in content or [])
    journal = os.environ.get("DYNA_AGENT_JOURNAL", "")
    if not journal:
        found = re.search(r"Run-owned journal path:\s*(\S+)", text)
        journal = found.group(1) if found else ""
    before = os.path.getsize(journal) if journal and os.path.exists(journal) else 0
    instructions = ""
    for index, arg in enumerate(sys.argv[1:]):
        if arg == "--append-system-prompt-file":
            with open(sys.argv[index + 2]) as source:
                instructions = source.read()
        elif arg == "--append-system-prompt":
            instructions = sys.argv[index + 2]
    worker = subprocess.Popen(["/bin/sh", "-c", body], stdin=subprocess.DEVNULL,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                             env=dict(os.environ, DYNA_FAKE_PROMPT=text, DYNA_FAKE_MODEL=model,
                                      DYNA_FAKE_ARGS="\n".join(sys.argv[1:]),
                                      DYNA_FAKE_INSTRUCTIONS=instructions))
    body_proc["p"] = worker
    worker_out, worker_err = worker.communicate()
    body_proc["p"] = None
    sys.stderr.write(worker_err or "")
    # A compliant worker writes its own journal entry; this fake does too, so a
    # turn never ends with the write-now reminder unless the body wrote one.
    if journal and os.path.getsize(journal) == before:
        with open(journal, "a") as log:
            log.write(json.dumps({"ts": int(__import__("time").time() * 1000), "kind": "verification",
                                  "message": "Fixture worker finished.", "source": "agent"}) + "\n")
    code = worker.returncode
    if worker_out:
        # A real worker says what it did before the turn ends.
        emit({"type": "assistant", "message": {"role": "assistant",
              "content": [{"type": "text", "text": worker_out}]}})
    result = {"type": "result", "session_id": "fake-worker",
              "subtype": "success" if code == 0 else "error_during_execution",
              "result": worker_out, "is_error": code != 0}
    if code != 0 and worker_err.strip():
        result["errors"] = [worker_err.strip()]
    if code != 0 and os.environ.get("DYNA_FAKE_STATUS"):
        # A provider's own status names the reason; prose does not.
        result["api_error_status"] = int(os.environ["DYNA_FAKE_STATUS"])
    emit(result)
sys.exit(code)
`
