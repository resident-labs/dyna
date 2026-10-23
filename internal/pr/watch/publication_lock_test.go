package watch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestPublicationDoesNotBlockOtherPRStoreOperations(t *testing.T) {
	for _, review := range []bool{false, true} {
		t.Run(fmt.Sprintf("review=%v", review), func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			entered, reply := make(chan struct{}), make(chan struct{})
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				close(entered)
				select {
				case <-reply:
				case <-ctx.Done():
					return ctx.Err()
				}
				_, err := fmt.Fprint(cmd.Stdout, `{"id":123}`)
				return err
			}
			published := make(chan error, 1)
			go func() {
				if review {
					_, err := e.s.PostReview(t.Context(), refRecord(r), e.author.ID, "review", r.Head, nil, true)
					published <- err
				} else {
					published <- e.s.Comment(t.Context(), refRecord(r), e.author.ID, "comment", true)
				}
			}()
			<-entered
			read, updated := make(chan error, 1), make(chan error, 1)
			go func() { _, err := e.s.Store.Read(); read <- err }()
			go func() {
				updated <- e.s.Store.update(func(st *State) error {
					st.Records["other"] = &Record{Key: "other", State: "open", Watching: true}
					return nil
				})
			}()
			blocked := ""
			readFinished, updateFinished := false, false
			select {
			case err := <-read:
				readFinished = true
				if err != nil {
					blocked = err.Error()
				}
			case <-time.After(time.Second):
				blocked = "plain state read"
			}
			select {
			case err := <-updated:
				updateFinished = true
				if err != nil {
					blocked = err.Error()
				}
			case <-time.After(time.Second):
				blocked = "another PR update"
			}
			close(reply)
			if err := <-published; err != nil {
				t.Fatal(err)
			}
			if !readFinished {
				<-read
			}
			if !updateFinished {
				<-updated
			}
			if blocked != "" {
				t.Fatalf("publication blocked %s", blocked)
			}
		})
	}
}

func TestCommentDuringPublicationWaitsForReviewerRouting(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	reviewer := "ses_20261017-000003-cccccccc"
	entered, reply := make(chan struct{}), make(chan struct{})
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		close(entered)
		select {
		case <-reply:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := fmt.Fprint(cmd.Stdout, `{"id":123}`)
		return err
	}
	published := make(chan error, 1)
	go func() {
		_, err := e.s.PostReview(t.Context(), refRecord(r), reviewer, "review finding", r.Head, nil, true)
		published <- err
	}()
	<-entered
	snap := Snapshot{Pull: e.f.Pull, Comments: []Comment{{Association: "COLLABORATOR", ID: 123, Kind: "reviews", Body: "review finding", User: User{Login: "owner", Type: "User"}, State: "COMMENTED"}}}
	processed := make(chan error, 1)
	process := func() error { return e.s.process(t.Context(), r.Key, snap, "pending", "pending", nil, nil, false) }
	go func() { processed <- process() }()
	finished := false
	select {
	case err := <-processed:
		finished = true
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
	}
	before, err := e.s.Store.Get(r.Key)
	if err != nil {
		t.Error(err)
	} else if _, seen := before.Seen["reviews:123"]; seen {
		t.Error("comment was processed before its reviewer routing ID was stored")
	}
	close(reply)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if !finished {
		if err := <-processed; err != nil {
			t.Fatal(err)
		}
	}
	if err := process(); err != nil {
		t.Fatal(err)
	}
	after := e.record()
	if decision := after.CommentDecisions["reviews:123"]; decision.Reason != "possibly_useful_feedback" {
		t.Fatalf("reviewer comment routed as owner feedback: %+v", decision)
	}
	st, err := e.s.Store.Read()
	if err != nil || st.CommentSessions["reviews:123"] != reviewer {
		t.Fatalf("reviewer routing lost: %v", err)
	}
}

func TestPublicationTimeoutReleasesItsGate(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	e.s.GH.Timeout = 25 * time.Millisecond
	e.s.GH.run = func(ctx context.Context, _ *exec.Cmd) error { <-ctx.Done(); return ctx.Err() }
	err := e.s.Comment(t.Context(), refRecord(r), e.author.ID, "comment", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("publication timeout: %v", err)
	}
	unlock, owned, err := e.s.tryPublicationGate(r.Key)
	if err != nil || !owned {
		t.Fatalf("timed-out publication retained the gate: %v", err)
	}
	unlock()
}
