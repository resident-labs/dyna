package voice

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDelegation(t *testing.T) {
	if got := delegation("ask <this>", "", false); got != "<realtime_delegation>\n  <input>ask &lt;this&gt;</input>\n</realtime_delegation>" {
		t.Fatal(got)
	}
	got := delegation("ask", "user: do it\nassistant: yes", false)
	if !strings.Contains(got, "<transcript_delta>user: do it\nassistant: yes</transcript_delta>") {
		t.Fatal(got)
	}
	got = delegation(strings.Repeat("é", 5000), strings.Repeat("ö", 5000), false)
	if len(got) > 8300 || !strings.Contains(got, "…</input>") || !strings.Contains(got, "<transcript_delta>…") {
		t.Fatal("field bounds")
	}
}

func TestStartupAndEnd(t *testing.T) {
	for _, speech := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "tail"}[speech], func(t *testing.T) {
			page, browser := pair(t)
			provider, fake := pair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ended := make(chan string, 1)
			go run(ctx, page, provider, testDispatch(func(context.Context, string) (string, error) { return "", nil }), nil, callLimits{time.Minute, time.Hour, time.Hour}, Options{Startup: strings.Repeat("old ", 6000) + "newest", End: func(ctx context.Context, tail string) error {
				if ctx.Err() != nil {
					t.Error(ctx.Err())
				}
				ended <- tail
				return nil
			}})
			expect(t, fake, "session.update")
			item := expect(t, fake, "conversation.item.create")["item"].(map[string]any)
			if item["role"] != "developer" {
				t.Fatal(item)
			}
			text := item["content"].([]any)[0].(map[string]any)["text"].(string)
			if len(text) > 20000 || !strings.Contains(text, "…output truncated…") || !strings.Contains(text, "newest") {
				t.Fatal("startup budget")
			}
			if speech {
				send(t, fake, message{"type": "conversation.item.input_audio_transcription.delta", "item_id": "u", "delta": "please "})
				expect(t, browser, "transcript")
				send(t, fake, message{"type": "conversation.item.input_audio_transcription.completed", "item_id": "u", "transcript": "please do this"})
				expect(t, browser, "transcript")
			}
			send(t, browser, message{"type": "closed"})
			select {
			case tail := <-ended:
				if speech != strings.Contains(tail, "user: please do this") {
					t.Fatal(tail)
				}
			case <-time.After(time.Second):
				t.Fatal("end not delivered")
			}
		})
	}
	var transcript callTranscript
	transcript.update("u", "owner", "done", true)
	transcript = callTranscript{} // successful handoff consumes the active transcript
	transcript.update("a", "assistant", "acknowledged", true)
	if transcript.end() != "" {
		t.Fatal("assistant-only tail")
	}
	if startupItem("") != nil {
		t.Fatal("empty startup")
	}
}
