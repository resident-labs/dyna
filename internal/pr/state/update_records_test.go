package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
)

func countUpdateRecordCopies(t *testing.T) *int {
	t.Helper()
	copies := new(int)
	read := copyForRead
	copyForRead = func(st State) State { *copies += len(st.Records); return read(st) }
	record := copyForRecord
	copyForRecord = func(r *Record) *Record { *copies++; return record(r) }
	t.Cleanup(func() { copyForRead = read; copyForRecord = record })
	return copies
}

func TestUpdateRecordsCopiesOnlyDeclaredRows(t *testing.T) {
	for _, count := range []int{0, 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root := newRoot(t)
			if err := Write(root, bigState(20)); err != nil {
				t.Fatal(err)
			}
			copies := countUpdateRecordCopies(t)
			var keys []string
			if count > 0 {
				keys = []string{"github.com/acme/widgets#7", "github.com/acme/widgets#7", "missing"}
			}
			err := UpdateRecords(t.Context(), root, keys, func(st *State) error {
				st.Reviewers["room"] = Reviewer{Profile: "changed"}
				if count > 0 {
					st.Records[keys[0]].Title = "changed"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if *copies != count {
				t.Fatalf("declared %d existing rows copied %d records; want %d", count, *copies, count)
			}
		})
	}
}

func TestGenericUpdateStillCopiesEveryRecord(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(20)); err != nil {
		t.Fatal(err)
	}
	copies := countUpdateRecordCopies(t)
	if err := Update(t.Context(), root, func(st *State) error {
		if len(st.Records) != 20 {
			return fmt.Errorf("generic callback saw %d records", len(st.Records))
		}
		st.Records["github.com/acme/widgets#7"].Title = "generic change"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if *copies != 20 {
		t.Fatalf("generic update made %d deep record copies; want 20", *copies)
	}
}

func TestUpdateRecordsSeesOnlyDeclaredRowsAndKeepsOtherRows(t *testing.T) {
	root := newRoot(t)
	initial := cloneFixture(2)
	initial.Records["other"] = &Record{Key: "other", State: "open", Notices: []Notice{{Text: "untouched"}}}
	initial.Responses["other"] = map[string]CachedResponse{"/other": {ETag: "untouched", Body: json.RawMessage(`{"old":true}`)}}
	if err := Write(root, initial); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	err = UpdateRecords(t.Context(), root, []string{"pr"}, func(st *State) error {
		if len(st.Records) != 1 || st.Records["pr"] == nil || st.Records["other"] != nil || st.Responses["other"] != nil {
			return fmt.Errorf("callback can see undeclared rows: records=%d, responses=%d", len(st.Records), len(st.Responses))
		}
		st.Records["pr"].Findings[0].Replies[0].Message = "changed"
		st.Records["pr"].ReviewAssignments["head"][0] = "changed"
		response := st.Responses["pr"]["/pulls/1"]
		response.Body[0], response.Body[1] = '[', ']'
		st.Responses["pr"]["/pulls/1"] = response
		st.Reviewers["room"] = Reviewer{Profile: "changed"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := before.Record("pr")
	if old.Findings[0].Replies[0].Message != "fixed" || old.ReviewAssignments["head"][0] != "reviewer" || before.Responses("pr")["/pulls/1"].Body[0] != '{' {
		t.Fatal("scoped update changed a retained snapshot")
	}
	if after.Records["pr"].Findings[0].Replies[0].Message != "changed" || after.Reviewers["room"].Profile != "changed" || after.Records["other"].Notices[0].Text != "untouched" || after.Responses["other"]["/other"].ETag != "untouched" {
		t.Fatal("scoped update lost declared changes or untouched rows")
	}
}

func TestUpdateRecordsRefusalDoesNotPublishOrCommit(t *testing.T) {
	for _, kind := range []string{"callback", "record", "response"} {
		t.Run(kind, func(t *testing.T) {
			root := newRoot(t)
			if err := Write(root, cloneFixture(2)); err != nil {
				t.Fatal(err)
			}
			before, err := ReadSnapshot(root)
			if err != nil {
				t.Fatal(err)
			}
			original, err := json.Marshal(before.Copy())
			if err != nil {
				t.Fatal(err)
			}
			var events []hub.Event
			writer := appstate.For(mustOpen(t, root))
			writer.SetPublish(func(event hub.Event) { events = append(events, event) })
			sentinel := errors.New("callback refused")
			err = UpdateRecords(t.Context(), root, []string{"pr"}, func(st *State) error {
				st.Records["pr"].Findings[0].Replies[0].Message = "must roll back"
				st.Reviewers["room"] = Reviewer{Profile: "must roll back"}
				switch kind {
				case "callback":
					return sentinel
				case "record":
					st.Records["undeclared"] = &Record{Key: "undeclared", State: "open"}
				case "response":
					st.Responses["undeclared"] = map[string]CachedResponse{"/bad": {ETag: "bad"}}
				}
				return nil
			})
			if err == nil || kind == "callback" && !errors.Is(err, sentinel) || kind != "callback" && !strings.Contains(err.Error(), "undeclared") {
				t.Fatalf("%s refusal = %v", kind, err)
			}
			after, readErr := ReadSnapshot(root)
			if readErr != nil {
				t.Fatal(readErr)
			}
			actual, marshalErr := json.Marshal(after.Copy())
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if after.Revision() != before.Revision() || string(actual) != string(original) || len(events) != 0 {
				t.Fatalf("refused update changed state or published: revision %s -> %s, events=%v", before.Revision(), after.Revision(), events)
			}
		})
	}
}

func TestUpdateRecordsAddsAndDeletesDeclaredMissingKeys(t *testing.T) {
	root := newRoot(t)
	initial := bigState(1)
	initial.Records["other"] = &Record{Key: "other", ChangeID: "missing", State: "open"}
	if err := Write(root, initial); err != nil {
		t.Fatal(err)
	}
	err := UpdateRecords(t.Context(), root, []string{"missing"}, func(st *State) error {
		if st.Records["missing"] != nil || len(st.Records) != 0 {
			return fmt.Errorf("missing key resolved another row")
		}
		st.Records["missing"] = &Record{Key: "missing", State: "open", Title: "new"}
		st.Responses["missing"] = map[string]CachedResponse{"/new": {ETag: "new"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !retained.HasRecord("missing") {
		t.Fatal("declared missing row was not added")
	}
	if err := UpdateRecords(t.Context(), root, []string{"missing"}, func(st *State) error {
		delete(st.Records, "missing")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Records["missing"] != nil || after.Responses["missing"] != nil || after.Records["other"] == nil || !retained.HasRecord("missing") {
		t.Fatal("declared deletion changed unrelated rows, left responses, or changed a retained snapshot")
	}
}

func TestUpdateRecordsConcurrentWorkersKeepBothChanges(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			key := fmt.Sprintf("github.com/acme/widgets#%d", i)
			results <- UpdateRecords(ctx, root, []string{key}, func(st *State) error {
				st.Records[key].Title = fmt.Sprintf("worker-%d", i)
				return nil
			})
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { workers.Wait(); close(results); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("scoped writers did not finish")
	}
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		key := fmt.Sprintf("github.com/acme/widgets#%d", i)
		if after.Records[key].Title != fmt.Sprintf("worker-%d", i) {
			t.Fatal("concurrent scoped write lost another worker change")
		}
	}
}

func TestUpdateRecordsMetadataQuotaKeepsRetainedSnapshot(t *testing.T) {
	root := newRoot(t)
	initial := bigState(2)
	remaining := 10
	scope := RateLimitScope{Host: "github.com", Account: "alice", Resource: "core"}
	initial.RateLimits = map[string]RateLimit{scope.Key(): {Remaining: &remaining}}
	for _, record := range initial.Records {
		record.Host, record.QuotaAccount = "github.com", "alice"
	}
	if err := Write(root, initial); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	copies := countUpdateRecordCopies(t)
	if err := UpdateRecords(t.Context(), root, nil, func(st *State) error {
		quota := st.RateLimits[scope.Key()]
		*quota.Remaining = 5
		st.RateLimits[scope.Key()] = quota
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if *copies != 0 {
		t.Fatalf("metadata quota update copied %d records; want 0", *copies)
	}
	after, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := before.Record("github.com/acme/widgets#0")
	fresh, _ := after.Record("github.com/acme/widgets#0")
	if *old.RateLimit.Remaining != 10 || *fresh.RateLimit.Remaining != 5 {
		t.Fatal("quota normalization mutated a retained record")
	}
	if got := len(after.ChangedKeys(before)); got != 2 {
		t.Fatalf("quota changed keys = %d, want 2", got)
	}
	if changedRows(t, root) != 0 {
		t.Fatal("quota-only update stored record rows")
	}
	copy := before.Copy()
	if !reflect.DeepEqual(copy.RateLimits[scope.Key()].Remaining, &remaining) {
		t.Fatal("metadata callback changed retained quota")
	}
}

func TestUpdateRecordsPrunesUntouchedRowsWithoutChangingRetainedSnapshot(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	expired := Record{Key: "github.com/acme/widgets#1", State: "closed", ClosedAt: time.Now().Add(-15 * 24 * time.Hour)}
	body, parts, err := splitStored(diskRecord{Record: &expired, Responses: map[string]CachedResponse{"/old": {ETag: "old"}}})
	if err != nil {
		t.Fatal(err)
	}
	writer := appstate.For(mustOpen(t, root))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := writer.Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "UPDATE pr_rows SET body=?, rev=rev+1 WHERE key=?", body, expired.Key); err != nil {
			return err
		}
		for name, part := range parts {
			if _, err := conn.ExecContext(ctx, "INSERT INTO pr_record_parts(key,part,body) VALUES(?,?,?) ON CONFLICT(key,part) DO UPDATE SET body=excluded.body", expired.Key, string(name), part); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, "UPDATE pr_state SET revision=revision+1 WHERE id=1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !before.HasRecord(expired.Key) {
		t.Fatal("expired fixture row is missing")
	}
	copies := countUpdateRecordCopies(t)
	if err := UpdateRecords(ctx, root, nil, func(st *State) error {
		st.Reviewers["room"] = Reviewer{Profile: "metadata"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if *copies != 0 {
		t.Fatalf("pruning untouched rows made %d deep record copies; want 0", *copies)
	}
	after, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.HasRecord(expired.Key) || !after.HasRecord("github.com/acme/widgets#0") || !before.HasRecord(expired.Key) || before.Responses(expired.Key)["/old"].ETag != "old" {
		t.Fatal("pruning lost a live row or changed a retained snapshot")
	}
}

func TestSelectedUpdateKeepsRetainedSnapshot(t *testing.T) {
	root := newRoot(t)
	initial := bigState(2)
	keys := make([]string, 0, len(initial.Records))
	for key := range initial.Records {
		keys = append(keys, key)
	}
	selected, other := keys[0], keys[1]
	initial.Records[selected].CIBudgetNotes = map[string]string{"nested": "old"}
	if err := Write(root, initial); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	oldOther := before.state.Records[other]
	err = UpdateSelectedRecords(t.Context(), root, func(row RecordScope) bool { return row.Key == selected }, func(st *State) error {
		st.Records[selected].CIBudgetNotes["nested"] = "new"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := before.Record(selected)
	fresh, _ := after.Record(selected)
	if old.CIBudgetNotes["nested"] != "old" || fresh.CIBudgetNotes["nested"] != "new" || after.state.Records[other] != oldOther {
		t.Fatal("selected update changed a retained snapshot or replaced the unaffected record")
	}
}
