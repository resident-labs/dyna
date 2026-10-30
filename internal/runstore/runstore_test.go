package runstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
)

func TestCreateCapturesSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-captured")
	t.Setenv(SessionEnv, strings.Repeat("s", 160))
	run, err := Create("session", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	if got := run.Meta.Session; got != strings.Repeat("s", 128) {
		t.Fatalf("captured session = %q (len %d)", got, len(got))
	}
	stored, err := ReadMeta(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Session != run.Meta.Session {
		t.Fatalf("stored session = %q, want %q", stored.Session, run.Meta.Session)
	}
}

func TestCreateRoutesResultToRequestedSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_target")
	t.Setenv("DYNA_RUN_RESULT_TO", "ses_target")
	run, err := Create("target", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Meta.ParentSession != "ses_target" {
		t.Fatalf("target = %q", run.Meta.ParentSession)
	}
	stored, err := ReadMeta(run.Meta.ID)
	if err != nil || stored.ParentSession != "ses_target" {
		t.Fatalf("stored target = %q, %v", stored.ParentSession, err)
	}
}

func TestCreateOmitsEmptySessionAndReadsOldMeta(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-empty")
	t.Setenv(SessionEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	run, err := Create("no session", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	b, err := json.Marshal(run.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"session"`) {
		t.Fatalf("empty session was persisted: %s", b)
	}

	var old Meta
	if err := json.Unmarshal([]byte(`{"id":"wf_old","name":"old","status":"ok","startedAt":"2026-01-01T00:00:00Z"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Session != "" || old.StartedAt != (time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("old meta = %#v", old)
	}
}

func TestOwnerFromEnv(t *testing.T) {
	t.Setenv(SessionEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if got := OwnerFromEnv(); got != "" {
		t.Fatalf("empty env: %q", got)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "c1")
	if got := OwnerFromEnv(); got != "claude:c1" {
		t.Fatalf("claude: %q", got)
	}
	t.Setenv(SessionEnv, "ses_x")
	if got := OwnerFromEnv(); got != "ses_x" {
		t.Fatalf("DYNA_SESSION wins: %q", got)
	}
}

func TestRunPublicationClearsOwnerDormancy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "ses_20261019-120000-abcdef12"
	t.Setenv("DYNA_SESSION", id)
	recordtest.WriteRaw(t, SessionsDir(), id, []byte(`{"id":"`+id+`","status":"dormant","finished":"2026-10-19T08:00:00Z"}`))
	run, err := Create("new work", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	raw, err := recordtest.ReadRaw(t, SessionsDir(), id)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status   string
		Finished time.Time
	}
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "idle" || !got.Finished.IsZero() {
		t.Fatalf("run owner = %s", raw)
	}
}
