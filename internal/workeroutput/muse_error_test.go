package workeroutput

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMuseRunErrorUsesTerminalStatusNotPartialText(t *testing.T) {
	for _, tc := range []struct{ name, stream, want string }{
		{"credit", `{"payload_type":"run.terminal.failed","payload":{"terminal":"failed","text":"credit balance exhausted"}}`, "credit balance exhausted"},
		{"structured detail", `{"payload_type":"run.terminal.failed","payload":{"error":{"message":"quota exhausted"}}}`, "quota exhausted"},
		{"empty failure", `{"payload_type":"run.terminal.failed"}`, "run.terminal.failed"},
		{"success", `{"payload_type":"run.terminal.completed","payload":{"text":"Tested usage limits and 429 errors."}}`, ""},
		{"ordinary mention", `{"payload_type":"run.output.delta","payload":{"text":"The quota test passes."}}`, ""},
		{"tool failure", `{"payload_type":"tool.result","payload":{"text":"exit code 1"}}`, ""},
		{"recovered", "{\"payload_type\":\"run.terminal.failed\",\"payload\":{\"text\":\"429 retrying\"}}\n{\"payload_type\":\"run.terminal.completed\",\"payload\":{\"text\":\"Done.\"}}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := MuseRunError(tc.stream)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
		})
	}
}

func TestMuseQuotaReset(t *testing.T) {
	raw, err := os.ReadFile("testdata/muse-quota.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	reset, ok := MuseQuotaReset(string(raw), "")
	if !ok || reset.Format(time.RFC3339) != "2026-10-19T00:00:00Z" {
		t.Fatal(reset, ok)
	}
	message := "429 Subscription quota exhausted. Your usage window resets at 2026-10-19T00:00:00Z"
	for _, stream := range []string{
		`{"payload_type":"run.output.delta","payload":{"text":"` + message + `"}}`,
		`{"payload_type":"tool.result","payload":{"text":"` + message + `"}}`,
		string(raw) + "\n" + `{"payload_type":"run.terminal.completed"}`,
	} {
		if _, ok := MuseQuotaReset(stream, ""); ok {
			t.Fatal("accepted prose, tool output, or recovered failure")
		}
	}
	if _, ok := MuseQuotaReset(`{"payload_type":"run.terminal.completed"}`, message); ok {
		t.Fatal("recorded quota after a successful retry")
	}
	if _, ok := MuseQuotaReset("", message); !ok {
		t.Fatal("lost stderr quota error")
	}
	if _, ok := MuseQuotaReset("", strings.ReplaceAll(message, "2026-10-19", "2026-99-99")); ok {
		t.Fatal("accepted invalid date")
	}
}
