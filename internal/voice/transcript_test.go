package voice

import (
	"strings"
	"testing"
)

func TestHallucinatedDropsNoiseLinesOnly(t *testing.T) {
	for _, c := range []struct {
		line  string
		noise bool
	}{
		{"What is CI doing?", false},
		// gpt-4o-mini-transcribe wrote this for 604 ms of noise 73 s after
		// Voice stopped (gateway.log, 2026-10-20T00:46:26Z).
		{"ChatGPT, a large language model trained by OpenAI.", true},
		{"Mm-hmm.", false},
		// The model repeated #1053's free-text prompt for 781 ms of noise
		// 16 s after Voice stopped (gateway.log, 2026-10-20T00:56:52Z).
		{"A developer talks to Voice, the voice of Dyna, a multi-agent coding tool. Expect words such as Dyna, root, agent, session, room, PR, CI, TUI, worktree, branch, merge, rebase, qwen, Codex, Claude and GitHub.", true},
		// The current prompt, repeated in part.
		{"Dyna, root, agent, PR.", true},
		{"Ask root why the Dyna CI is red.", false},
		{"Root.", false},
	} {
		if got := hallucinated(c.line); got != c.noise {
			t.Errorf("hallucinated(%q) = %v, want %v", c.line, got, c.noise)
		}
	}
	for _, word := range []string{"Dyna", "root", "PR", "CI", "TUI", "worktree"} {
		if !strings.Contains(TranscriptionPrompt, word) {
			t.Errorf("the transcription prompt lacks %q", word)
		}
	}
}
