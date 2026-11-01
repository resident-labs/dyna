package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// fixtureTurns is invented work for comparing long session-thread layouts.
// The API returns newest turns first. Nothing here is real repository data.
func fixtureTurns() []contract.TranscriptTurn {
	requests := []string{
		"Show a retry hint when a sync fails, and add a focused test.",
		"Can you also mention that retry is safe?",
		"Check the hint on a narrow screen.",
		"Check the success message remains unchanged.",
		"Review the retry wording for accessibility.",
		"Check the sync client behavior.",
		"Review the error state.",
		"Check the focused test coverage.",
		"Inspect the banner layout.",
		"Check the documentation.",
		"Run the layout check again.",
		"Check whether the hint wraps on narrow screens.",
	}
	tools := []struct{ name, input, output string }{
		{"grep", "syncStatus|SyncStatus", "src/sync/status.ts:18:export function syncStatus\nsrc/ui/SyncBanner.tsx:24: const status = syncStatus(result)"},
		{"read", "src/sync/status.ts", "return result.ok ? 'ready' : 'failed';"},
		{"edit", "src/ui/SyncBanner.tsx", "- Sync failed\n+ Sync failed. Check your connection. It is safe to try again."},
		{"bash", "npm test -- sync-status.test.ts", "Test Files  1 passed (1)\nTests       3 passed (3)"},
	}
	// Dyna: The first turn matches the reference timeline capture.
	referenceTools := []struct{ name, input, output string }{
		{"bash", "rg -n 'syncStatus|SyncStatus' src test", "src/sync/status.ts:18:export function syncStatus\nsrc/ui/SyncBanner.tsx:24: const status = syncStatus(result)\ntest/sync-status.test.ts:8:describe('sync status'"},
		{"read", "src/sync/status.ts", "export function syncStatus(result: SyncResult) {\n  return result.ok ? 'ready' : 'failed';\n}"},
		{"read", "src/ui/SyncBanner.tsx", "return <p>{status === 'failed' ? 'Sync failed' : 'Up to date'}</p>;"},
		{"grep", "retry in src and test", "src/sync/client.ts:42: retrySync()\ntest/sync-client.test.ts:61: retries transient errors"},
		{"bash", "git status --short", ""},
		{"edit", "src/ui/SyncBanner.tsx", "--- a/src/ui/SyncBanner.tsx\n+++ b/src/ui/SyncBanner.tsx\n@@ -21,7 +21,9 @@\n- Sync failed\n+ Sync failed. Check your connection and try again.\n+ Your work is safe.\n+ Retry when ready."},
		{"edit", "test/sync-status.test.ts", "--- a/test/sync-status.test.ts\n+++ b/test/sync-status.test.ts\n@@ -12,3 +12,7 @@\n+  it('shows a retry hint after failure', () => {\n+    expect(renderFailure()).toContain('try again');\n+  });"},
		{"bash", "npm test -- sync-status.test.ts", "✓ test/sync-status.test.ts (3 tests)\nTest Files  1 passed (1)\nTests       3 passed (3)"},
		{"bash", "git diff --stat", " src/ui/SyncBanner.tsx    | 4 +++-\n test/sync-status.test.ts | 4 ++++\n 2 files changed, 7 insertions(+), 1 deletion(-)"},
	}
	base := time.Now().Add(-2 * time.Hour)
	turns := make([]contract.TranscriptTurn, 0, len(requests))
	for i, request := range requests {
		id := contract.TurnID(fmt.Sprintf("fixture-turn-%02d", i+1))
		start := base.Add(time.Duration(i) * 8 * time.Minute)
		duration := 2 * time.Minute
		if i == 0 {
			duration = 114 * time.Second
		}
		end := start.Add(duration)
		turn := contract.TranscriptTurn{ID: id, Started: start, Ended: &end, DurationMS: duration.Milliseconds(), Outcome: contract.TurnCompleted,
			Inputs: []contract.TranscriptInput{{ID: contract.EventID(fmt.Sprintf("fixture-input-%d", i)), At: start, Type: contract.EventType("post.created"), From: contract.Principal{Kind: contract.PrincipalOwner}, Text: request}}}
		turn.Items = append(turn.Items, contract.TranscriptItem{ID: fmt.Sprintf("%s-text", id), Turn: id, Kind: contract.TranscriptText, At: start, Text: "I’ll trace the sync state and its current tests before changing the UI."})
		turnTools := tools
		if i == 0 {
			turnTools = referenceTools
		}
		for j, tool := range turnTools {
			at := start.Add(time.Duration(j+1) * 10 * time.Second)
			done := at.Add(time.Second)
			key := "path"
			if tool.name == "bash" {
				key = "command"
			} else if tool.name == "grep" {
				key = "pattern"
			}
			args, _ := json.Marshal(map[string]string{key: tool.input})
			item := contract.TranscriptItem{ID: fmt.Sprintf("%s-tool-%d", id, j), Turn: id, Kind: contract.TranscriptTool, At: at, Ended: &done, Tool: tool.name, Arguments: string(args), Output: tool.output}
			if tool.name == "edit" {
				item.Diff = tool.output
			}
			if i == 10 && j == 3 {
				item.Failed = true
				item.Output = "Layout check failed: the test browser could not start."
			}
			if i == 11 && j == 3 {
				item.Arguments = `{"command":"npm run test:layout -- --width 320"}`
				item.Output = "Checking sync banner…"
				item.Ended = nil
			}
			if i >= 1 && i <= 3 && j == 1 {
				item.Posts = []contract.TranscriptItem{{ID: fmt.Sprintf("%s-post", id), Turn: id, Kind: contract.TranscriptPost, At: at, Text: []string{"I found the sync status and its banner.", "The retry wording is ready for review.", "I am checking the narrow layout."}[i-1]}}
			}
			turn.Items = append(turn.Items, item)
		}
		if i == 0 {
			turn.Items = turn.Items[:1]
			for j := range 3 {
				at := start.Add(time.Duration(j+1) * 10 * time.Second)
				done := at.Add(time.Second)
				script := "const result = await tools.read({ path: \"src/sync/status.ts\" });\ntext(result);"
				if j == 2 {
					script = strings.Repeat("const step = await tools.read({ path: \"src/sync/status.ts\" });\n", 16) + "text(step);"
				}
				calls := []contract.TranscriptCall{
					{Tool: "read", Target: "src/sync/status.ts", Status: contract.TranscriptCallOK},
					{Tool: "read", Target: "src/ui/SyncBanner.tsx", Status: contract.TranscriptCallOK},
					{Tool: "mcp__shell__shell", Target: "npm test", Status: contract.TranscriptCallOK},
				}
				if j == 2 {
					calls[0].Status, calls[0].Error = contract.TranscriptCallError, "The file was not found."
				}
				turn.Items = append(turn.Items, contract.TranscriptItem{ID: fmt.Sprintf("%s-script-%d", id, j), Turn: id, Kind: contract.TranscriptTool, At: at, Ended: &done, Tool: "codemode", Script: script, Calls: calls, Output: "Test Files  1 passed\nTests  3 passed\nDuration  0.4 s"})
			}
			turn.Items = append(turn.Items, contract.TranscriptItem{ID: "fixture-own-post", Turn: id, Kind: contract.TranscriptPost, At: end, From: &contract.Principal{Kind: contract.PrincipalSession, Session: fixtureSession}, Text: "The retry hint is ready for review.", Home: &contract.Conversation{Kind: contract.ConversationFeed}, Room: "fixture", Seq: 1})
		}
		if i == 10 {
			turn.Outcome, turn.Cause = contract.TurnFailed, contract.CauseModelFailed
		} else if i == 11 {
			turn.Ended, turn.Outcome, turn.DurationMS = nil, "", 0
		} else {
			final := "Updated the hint to say that it is safe to try again. The focused tests pass."
			if i == 0 {
				final = "## Done\n\nThe sync banner now tells people what to do when a sync fails. It asks them to check their connection and try again. The success message stays the same.\n\n### Changes\n\n- Updated `src/ui/SyncBanner.tsx` to show the retry hint only for a failed sync.\n- Added a focused failure case in `test/sync-status.test.ts`.\n- Kept the existing sync client and retry behavior unchanged.\n\n### Verification\n\n`npm test -- sync-status.test.ts` passed all three tests.\n\nThe hint is plain text, so it remains available to assistive technology. No account data or network request is included in the test."
			}
			turn.Items = append(turn.Items, contract.TranscriptItem{ID: fmt.Sprintf("%s-final", id), Turn: id, Kind: contract.TranscriptText, At: end, Text: final})
		}
		turns = append([]contract.TranscriptTurn{turn}, turns...)
	}
	return turns
}
