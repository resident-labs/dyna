package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/websvc"
)

// A fixture admission method exercises the production dispatcher, binder,
// socket and request store. Reusing an ID must compare the call before
// returning any stored result, even when only the mutation receipt exists.
func TestAdmissionReplayChecksFactsBeforeCachedResponse(t *testing.T) {
	type clientFacts struct {
		WorkDir string
		Account string
		Profile string
	}
	type admitted struct {
		Facts      clientFacts
		AdmittedAt time.Time
	}
	for _, lostResult := range []bool{false, true} {
		name := "stored result"
		if lostResult {
			name = "commit before result"
		}
		t.Run(name, func(t *testing.T) {
			db, w, dataDir := thingsStore(t)
			firstTime := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
			now := firstTime
			handlers := 0
			calls, err := websvc.Bind([]websvc.Binding{{
				Name: "AdmitAccountAttempt", Access: websvc.Write, Scope: websvc.ScopeOperate,
				Method: func(ctx context.Context, workDir, account, profile string) (admitted, error) {
					handlers++
					facts := clientFacts{workDir, account, profile}
					var raw string
					err := db.QueryRowContext(ctx, "SELECT name FROM things LIMIT 1").Scan(&raw)
					if err == nil {
						var previous admitted
						if err := json.Unmarshal([]byte(raw), &previous); err != nil {
							return admitted{}, err
						}
						if previous.Facts != facts {
							return admitted{}, errors.New("request ID was reused with different method or arguments")
						}
						return previous, nil
					}
					if !errors.Is(err, sql.ErrNoRows) {
						return admitted{}, err
					}
					fact := admitted{Facts: facts, AdmittedAt: now}
					body, err := json.Marshal(fact)
					if err != nil {
						return admitted{}, err
					}
					err = w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
						_, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES(?)", string(body))
						return err
					})
					return fact, err
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			calls["OtherAdmission"] = calls["AdmitAccountAttempt"]
			applyOnce(calls, w, t.Logf)
			enginetest.Serve(t, dataDir, calls)
			client := engineclient.New("")
			ctx := callspec.WithRequest(t.Context(), "ses_example/1/123/456/7")
			original := clientFacts{"/repo", "account-a", "muse"}
			send := func(facts clientFacts) (admitted, error) {
				var got admitted
				err := client.Call(ctx, "AdmitAccountAttempt", []any{facts.WorkDir, facts.Account, facts.Profile}, &got)
				return got, err
			}
			first, err := send(original)
			if err != nil || !first.AdmittedAt.Equal(firstTime) {
				t.Fatalf("initial admission: %+v %v", first, err)
			}
			if lostResult {
				// The request write committed but its result was not saved.
				if err := w.Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(t.Context(), "UPDATE request_ids SET result=NULL")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			now = firstTime.Add(time.Hour)
			again, err := send(original)
			if err != nil {
				t.Fatalf("unchanged replay: %v", err)
			}
			if !lostResult && !again.AdmittedAt.Equal(firstTime) {
				t.Errorf("replay replaced admitted_at: %v", again.AdmittedAt)
			}
			changes := map[string]clientFacts{
				"work_dir": {"/other-repo", original.Account, original.Profile},
				"account":  {original.WorkDir, "account-b", original.Profile},
				"profile":  {original.WorkDir, original.Account, "other-profile"},
			}
			for field, facts := range changes {
				t.Run(field, func(t *testing.T) {
					if _, err := send(facts); err == nil || !strings.Contains(err.Error(), "request ID was reused with different method or arguments") {
						t.Errorf("same ID with changed %s returned cached success: %v", field, err)
					}
				})
			}
			var changedMethod admitted
			err = client.Call(ctx, "OtherAdmission", []any{original.WorkDir, original.Account, original.Profile}, &changedMethod)
			if err == nil || !strings.Contains(err.Error(), "request ID was reused with different method or arguments") {
				t.Errorf("same ID with changed method returned cached success: %v", err)
			}
			if handlers != 1 {
				t.Errorf("handler ran %d times; cached replays must not rerun admission", handlers)
			}
			var raw string
			if err := db.QueryRow("SELECT name FROM things LIMIT 1").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var saved admitted
			if err := json.Unmarshal([]byte(raw), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Facts != original || !saved.AdmittedAt.Equal(firstTime) {
				t.Errorf("replay changed immutable admission: %+v", saved)
			}
		})
	}
}

// The same call may encode object keys in another order. Distinct large
// JSON integers must stay distinct even when float64 would round them.
func TestRequestFingerprintCanonicalJSON(t *testing.T) {
	db, w, dataDir := thingsStore(t)
	handlers := 0
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "SaveJSON", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, raw json.RawMessage) (int, error) {
			handlers++
			err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES(?)", string(raw))
				return err
			})
			return handlers, err
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, w, t.Logf)
	enginetest.Serve(t, dataDir, calls)
	client := engineclient.New("")
	raw := func(s string) json.RawMessage { return json.RawMessage(strings.ReplaceAll(s, "'", string(rune(34)))) }
	send := func(id, input string) (int, error) {
		var result int
		err := client.Call(callspec.WithRequest(t.Context(), id), "SaveJSON", []any{raw(input)}, &result)
		return result, err
	}
	first, err := send("object", "{'b':1,'a':[9007199254740993]}")
	if err != nil {
		t.Fatal(err)
	}
	again, err := send("object", " { 'a' : [9007199254740993], 'b' : 1 } ")
	if err != nil || again != first {
		t.Fatalf("canonical object replay: %d %v", again, err)
	}
	for i, pair := range [][2]string{
		{"9007199254740992", "9007199254740993"},
		{"1000000000000000128", "1000000000000000100"},
	} {
		id := "integer-" + string(rune('a'+i))
		if _, err := send(id, pair[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := send(id, pair[1]); err == nil || !strings.Contains(err.Error(), "request ID was reused with different method or arguments") {
			t.Errorf("distinct integers shared a request fingerprint: %s / %s: %v", pair[0], pair[1], err)
		}
	}
	if handlers != 3 || countRows(t, db) != 3 {
		t.Errorf("canonical replay ran extra writes: %d handlers, %d rows", handlers, countRows(t, db))
	}
}

// A side-effect-only call can find a competing receipt at result finish,
// after its initial cache miss. A conflict must reach the caller as an error.
func TestRequestFinishConflictDoesNotReturnSuccess(t *testing.T) {
	_, w, dataDir := thingsStore(t)
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "ResultOnly", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, value string) (string, error) {
			competing, _ := json.Marshal("other")
			fingerprint, err := requestFingerprint("ResultOnly", []json.RawMessage{competing})
			if err != nil {
				return "", err
			}
			key := requestKey(ctx, callspec.RequestOf(ctx))
			other := state.ForRequestCall(ctx, key, "ResultOnly", fingerprint)
			// Simulate another writer committing between the cache miss and finish.
			if err := w.FinishRequest(other, key, "ResultOnly", competing); err != nil {
				return "", err
			}
			return value, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, w, t.Logf)
	enginetest.Serve(t, dataDir, calls)
	var got string
	err = engineclient.New("").Call(callspec.WithRequest(t.Context(), "finish-race"), "ResultOnly", []any{"original"}, &got)
	if err == nil || !strings.Contains(err.Error(), "request ID was reused with different method or arguments") {
		t.Fatalf("finish conflict returned success %q: %v", got, err)
	}
}
