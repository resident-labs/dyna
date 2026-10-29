package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/roomsvc"
)

// TestClientCallsAreServedByTheEngine guards the oldest contract Dyna has: a
// session can outlive an engine self-update, so the dyna build it still runs
// must reach every call it sends. PR #345 renamed RecordPRVerdict and a live
// reviewer could not record its verdict afterwards ("engine dev+af9744a has no
// RecordPRVerdict method"). This test fails when a client names a call that the
// engine does not serve, so the rename lands with a shim instead.
//
// The test reads the client source, because the method name is a string at each
// call site. An engine request replayed from the outbox (engineclient) is not
// visible here: it was saved by an older build, and callspec.LegacyCalls is
// what keeps such a request working.
func TestClientCallsAreServedByTheEngine(t *testing.T) {
	served := map[string]bool{}
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		served[binding.Name] = true
	}
	// Every legacy name stays served, and so does the call it forwards to.
	for name, legacy := range callspec.LegacyCalls {
		if !served[name] {
			t.Errorf("legacy call %s is not served; keep its shim until the release in callspec.LegacyCalls", name)
		}
		if !served[legacy.Replacement] {
			t.Errorf("legacy call %s forwards to %s, which is not served", name, legacy.Replacement)
		}
	}
	sites, err := clientCallSites("../../internal", "../../cmd")
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]bool{}
	unlisted := map[string]bool{}
	for site := range dynamicCallSites {
		unlisted[site] = true
	}
	sawRecordReviewResult := false
	for _, site := range sites {
		if name, ok := quotedName(site.method); ok {
			// A literal name needs no table: the client says what it sends.
			if name == "RecordReviewResult" {
				sawRecordReviewResult = true
			}
			if !served[name] {
				missing[fmt.Sprintf("%s calls %s", site.at, name)] = true
			}
			continue
		}
		key := site.file + " " + site.method
		names, listed := dynamicCallSites[key]
		if !listed {
			t.Errorf("%s passes the method name %q; list the site and every name it can hold in dynamicCallSites", site.at, site.method)
			continue
		}
		delete(unlisted, key)
		for _, name := range names {
			if !served[name] {
				missing[fmt.Sprintf("%s can call %s", key, name)] = true
			}
		}
	}
	for site := range unlisted {
		t.Errorf("dynamicCallSites lists %s, which no client call site uses any more; remove the entry", site)
	}
	if !sawRecordReviewResult {
		t.Fatal("the walk found no call of RecordReviewResult; the client folders moved, so this guard would pass without checking anything")
	}
	for problem := range missing {
		t.Errorf("%s, which the engine does not serve; keep a legacy shim (callspec.LegacyCalls) or change the client", problem)
	}
}

// dynamicCallSites names each client call site whose method name is a variable,
// with every name that variable can hold. A helper that takes the name as an
// argument hides it from the walk, so the site is listed here and checked
// against the engine's table.
var dynamicCallSites = map[string][]string{
	"internal/cli/request/question.go method":       {"AskQuestion", "AnswerQuestion"},
	"internal/cli/hooks/hooks.go method":            {"RunHook", "TestHook"},
	"internal/cli/pr/pr.go call":                    {"MergeFixPR", "UntrackPR", "ClosePR", "ResumePR", "SetPRDraft", "RequirePRReviewers"},
	"internal/cli/room/pins.go call":                {"Pin", "Unpin"},
	"internal/cli/room/session_controls.go method":  {"PauseRoomSessions", "ResumeRoomSessions", "ClearDormantSessions"},
	"internal/cli/request/request.go method":        {"ApproveRequest", "DenyRequest", "WithdrawRequest"},
	"internal/cli/rules/rules.go method":            {"AddRule", "EditRule", "RemoveRule"},
	"internal/cli/subagent/consolidation.go method": {"ResumeSessionConsolidation", "CancelSessionConsolidation"},
	"internal/cli/sync/operation.go method":         {"SyncRefresh"},
}

// clientCallSite is one .Call( call in client source, with the text of its
// second argument: a quoted name, or the variable that holds the name.
type clientCallSite struct {
	at     string // file and line, for the failure message
	file   string // path from the repository root
	method string
}

// clientCallSites reads every non-test Go file under roots and returns each
// .Call( whose method argument is a quoted name or a plain variable. A call
// that spans lines is not seen; client code keeps its method name on the line
// that opens the call.
func clientCallSites(roots ...string) ([]clientCallSite, error) {
	var sites []clientCallSite
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file := strings.TrimPrefix(filepath.ToSlash(path), "../../")
			for i, line := range strings.Split(string(data), "\n") {
				method, ok := callMethodArgument(line)
				if !ok {
					continue
				}
				sites = append(sites, clientCallSite{at: fmt.Sprintf("%s:%d", file, i+1), file: file, method: method})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sites, nil
}

// callMethodArgument returns the second argument of the first .Call( on line,
// when that argument is a quoted name or a plain variable.
func callMethodArgument(line string) (string, bool) {
	start := strings.Index(line, ".Call(")
	if start < 0 {
		return "", false
	}
	args := splitArguments(line[start+len(".Call("):])
	if len(args) < 2 {
		return "", false
	}
	method := strings.TrimSpace(args[1])
	if _, ok := quotedName(method); ok {
		return method, true
	}
	// A plain variable holds the name; anything else (a struct literal, a
	// selector, a nested call) belongs to another client, such as the
	// computers API, which is not this socket.
	if method == "" || strings.ContainsAny(method, ".{(") {
		return "", false
	}
	return method, true
}

// quotedName reads a Go string literal that names a call.
func quotedName(arg string) (string, bool) {
	if len(arg) < 2 || arg[0] != '"' || arg[len(arg)-1] != '"' {
		return "", false
	}
	name := arg[1 : len(arg)-1]
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return "", false
	}
	return name, name != ""
}

// splitArguments splits text at the commas outside brackets and strings.
func splitArguments(text string) []string {
	var args []string
	depth := 0
	start := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '`' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return append(args, text[start:i])
			}
			depth--
		case c == ',' && depth == 0:
			args = append(args, text[start:i])
			start = i + 1
		}
	}
	return append(args, text[start:])
}
