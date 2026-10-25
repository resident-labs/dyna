package root

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workeroutput"
)

// service is a notify service over a fresh data dir whose sessions run mock
// turns in this process.
func service(t *testing.T) *notify.Service {
	t.Helper()
	// Session scratch must be removed with the fixture after its runners stop.
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	runners := &sync.WaitGroup{}
	t.Cleanup(func() { cancel(); runners.Wait() })
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true}}}
	mgr := &subagent.Manager{Store: subagent.DefaultStore(), Profiles: func() (*profile.Store, error) { return profiles, nil }, Settings: settings.Defaults, Spawner: subagent.InProcessSpawner{Ctx: ctx, Runners: runners}}
	svc := notify.New(room.DefaultStore(), mgr)
	serveSeatCalls(t, svc)
	return svc
}

// serveSeatCalls serves the engine's seat calls over svc from a fake engine.
// A call acts for whoever this test process poses as, as the engine acts for
// the caller its launch record names.
func serveSeatCalls(t *testing.T, svc *notify.Service) {
	t.Helper()
	calls, err := websvc.Bind(engine.SeatBindings(roomsvc.New(svc)))
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, authority.Current()), args)
		}
	}
	enginetest.Serve(t, store.DataDir(), calls)
}

func run(t *testing.T, svc *notify.Service, stdin string, args ...string) (string, error) {
	t.Helper()
	return runContext(t, context.Background(), svc, stdin, args...)
}

func runContext(t *testing.T, ctx context.Context, svc *notify.Service, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Command(Options{Service: func() *notify.Service { return svc }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func clearStamps(t *testing.T) {
	storetest.ClearStamp(t)
}

// An unstamped CLI attaches and detaches; status shows the holder.
func TestAttachDetachStatus(t *testing.T) {
	clearStamps(t)
	svc := service(t)
	if _, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct{ args, want string }{
		{"status --room alpha", "room alpha: root seat held by the internal root\n"},
		{"attach --room alpha", "room alpha: root seat held by the external orchestrator since "},
		{"attach --room alpha", "unchanged: room alpha: root seat held by the external orchestrator since "},
		{"status --room alpha", "external orchestrator since "},
		{"attach --room alpha --harness Claude", "room alpha: root seat held by the external orchestrator (claude) since "},
		{"status --room alpha", "external orchestrator (claude) since "},
		{"detach --room alpha", "room alpha: root seat held by the internal root since "},
	}
	for _, step := range steps {
		out, err := run(t, svc, "", strings.Fields(step.args)...)
		if err != nil || !strings.Contains(out, step.want) {
			t.Fatalf("%s = %q, %v; want %q", step.args, out, err, step.want)
		}
	}
}

// Stamped Dyna work cannot move the seat. The internal root receives posts as
// turn input, so it never attaches either.
func TestAttachRefusesDynaWork(t *testing.T) {
	svc := service(t)
	rec, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{mode.Subagent, mode.Root} {
		t.Run(m, func(t *testing.T) {
			clearStamps(t)
			t.Setenv(mode.EnvMode, m)
			t.Setenv(mode.EnvSession, "ses_20260101-120000-abcdef12")
			t.Setenv(mode.EnvRoomID, rec.ID)
			for _, verb := range []string{"attach", "detach"} {
				if out, err := run(t, svc, "", verb); err == nil {
					t.Fatalf("%s from %s mode = %q, want refusal", verb, m, out)
				}
			}
		})
	}
	if got, _ := svc.Rooms.Resolve(rec.ID); got.Seat.External || !got.Seat.Since.IsZero() {
		t.Fatalf("seat changed: %+v", got.Seat)
	}
}

// pendingRootTurn holds the real runner and its turn lock until the fixture
// chooses to launch it, as the detached runner can wait for process admission.
type pendingRootTurn struct {
	manager *subagent.Manager
	id      string
	n       int
	lock    *os.File
}

type rootTurnQueue chan pendingRootTurn

func (q rootTurnQueue) Spawn(m *subagent.Manager, id string, n int, lock *os.File) error {
	select {
	case q <- pendingRootTurn{m, id, n, lock}:
		return nil
	default:
		return fmt.Errorf("unexpected extra root turn %s/%d", id, n)
	}
}

func (q rootTurnQueue) next(t *testing.T) pendingRootTurn {
	t.Helper()
	select {
	case turn := <-q:
		return turn
	case <-time.After(30 * time.Second):
		t.Fatal("root turn was not queued")
		return pendingRootTurn{}
	}
}

// Attach stops a root turn before its harness starts. Detach with --handoff -
// reads the note from stdin and wakes a fresh internal root.
func TestAttachStopsTheInternalRootAndHandoffWakesIt(t *testing.T) {
	clearStamps(t)
	svc := service(t)
	pending := make(rootTurnQueue, 2)
	svc.Sessions.Spawner = pending
	t.Cleanup(func() {
		for {
			select {
			case turn := <-pending:
				turn.lock.Close()
			default:
				return
			}
		}
	})
	wait := func(event <-chan struct{}) {
		t.Helper()
		select {
		case <-event:
		case <-time.After(30 * time.Second):
			t.Fatal("root runner did not reach its fixture event")
		}
	}
	runBounded := func(stdin string, args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return runContext(t, ctx, svc, stdin, args...)
	}
	rec, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	// Hold process admission so resumable harness metadata cannot race attach.
	if _, _, err := svc.Sessions.StartRoot(rec, "SLEEP:0\nRESPOND: busy", ""); err != nil {
		t.Fatal(err)
	}
	root, ok, err := svc.Sessions.RootSession(rec.ID)
	if err != nil || !ok {
		t.Fatalf("no internal root: %v", err)
	}
	first := pending.next(t)
	if first.id != root.ID || first.n != 1 {
		first.lock.Close()
		t.Fatalf("queued root turn = %s/%d; want %s/1", first.id, first.n, root.ID)
	}
	db, err := database.Open(filepath.Dir(svc.Sessions.Store.Root))
	if err != nil {
		first.lock.Close()
		t.Fatal(err)
	}
	stopAsked := make(chan struct{})
	changed := make(chan struct{}, 1)
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) {
		if event.Topic == sessionlife.Topic && event.Key == root.ID {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	})
	t.Cleanup(func() { writer.SetPublish(func(hub.Event) {}) })
	runnerCtx, cancelRunner := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		// Read outside publication: ReadTurn may import a missing row.
	waitForStop:
		for {
			select {
			case <-changed:
				if turn, err := svc.Sessions.Store.ReadTurn(root.ID, 1); err == nil && turn.StopAsked {
					close(stopAsked)
					break waitForStop
				}
			case <-runnerCtx.Done():
				break waitForStop
			}
		}
		_ = first.manager.RunTurn(runnerCtx, first.id, first.n, first.lock)
	}()
	t.Cleanup(func() { cancelRunner(); wait(finished) })

	out, err := runBounded("", "attach", "--room", "alpha")
	if err != nil || !strings.Contains(out, "[DYNA ROOT SEAT HANDOFF]") {
		t.Fatalf("attach = %q, %v", out, err)
	}
	wait(stopAsked)
	wait(finished)
	if turn, err := svc.Sessions.Store.ReadTurn(root.ID, 1); err != nil || !turn.StopAsked || turn.Status != subagent.TurnStopped || turn.HarnessSession != "" {
		t.Fatalf("internal root did not stop before its harness started: %+v, %v", turn, err)
	}
	// A turn stopped before its harness reported a session cannot resume, so
	// the handoff then starts a fresh internal root.
	out, err = runBounded("SLEEP:0\nRESPOND: resumed", "detach", "--room", "alpha", "--handoff", "-")
	if err != nil || !strings.Contains(out, "handoff: alpha:root root-started") {
		t.Fatalf("detach = %q, %v", out, err)
	}
	next, ok, err := svc.Sessions.RootSession(rec.ID)
	if err != nil || !ok || next.ID == root.ID {
		t.Fatalf("no fresh internal root after the handoff: %+v, %v", next, err)
	}
	second := pending.next(t)
	if second.id != next.ID || second.n != next.Turns {
		second.lock.Close()
		t.Fatalf("queued handoff turn = %s/%d; want %s/%d", second.id, second.n, next.ID, next.Turns)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := second.manager.RunTurn(ctx, second.id, second.n, second.lock); err != nil {
		t.Fatal(err)
	}
	if turn, err := svc.Sessions.Store.ReadTurn(next.ID, next.Turns); err != nil || turn.Status != subagent.TurnOK || !strings.Contains(turn.Prompt+steering(turn), "[ROOT SEAT HANDOFF]") {
		t.Fatalf("handoff turn = %+v, %v", turn, err)
	}
}

func steering(turn subagent.Turn) string {
	var out string
	for _, st := range turn.Steering {
		out += st.Message
	}
	return out
}

// The harness name is one lowercase word, so the web app can map it to an icon.
func TestAttachRefusesBadHarnessName(t *testing.T) {
	clearStamps(t)
	svc := service(t)
	if _, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, svc, "", "attach", "--room", "alpha", "--harness", "claude code"); err == nil {
		t.Fatal("attach accepted a harness name with a space")
	}
}

func TestSeatChangesPostOneNoticeEach(t *testing.T) {
	clearStamps(t)
	svc := service(t)
	rec, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"attach", "--room", "alpha", "--harness", "pi"},
		{"attach", "--room", "alpha", "--harness", "pi"},
		{"detach", "--room", "alpha"},
	} {
		if _, err := run(t, svc, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	posts, err := svc.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want two seat notices: %+v", len(posts), posts)
	}
	for i, want := range []string{"pi (external) took the root seat", "Internal root took back the seat"} {
		p := posts[i]
		payload, ok := p.Session()
		if p.From != "dyna" || !ok || payload.Seat != want || p.Wake || !p.Addressed("alpha:root") || !p.Addressed("alpha") || !strings.HasPrefix(p.Text, want) {
			t.Fatalf("notice %d = %+v; want %q", i, p, want)
		}
		if !room.DefaultView(p, rec.Name) {
			t.Fatalf("watcher cannot see notice: %+v", p)
		}
	}
	if _, err := run(t, svc, "", "attach", "--room", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, svc, "PR 12 waits for CI.", "detach", "--room", "alpha", "--handoff", "-"); err != nil {
		t.Fatal(err)
	}
	posts, _ = svc.Rooms.Messages(rec.ID)
	var notices []room.Message
	for _, p := range posts {
		if p.Kind == "session" && p.Notice.Kind == "seat" {
			notices = append(notices, p)
		}
	}
	if len(notices) != 4 || !strings.HasPrefix(notices[2].Text, "An external orchestrator took the root seat") || !strings.Contains(notices[3].Text, "PR 12 waits for CI.") {
		t.Fatalf("handoff notices: %+v", notices)
	}
}

// An empty room changes seats without inventing a handoff.
func TestAttachWithoutInternalRootHasNoHandoff(t *testing.T) {
	clearStamps(t)
	svc := service(t)
	rec, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	svc.Sessions.TransferSleep = func(context.Context, time.Duration) error {
		t.Fatal("attach waited without an internal root")
		return nil
	}
	out, err := run(t, svc, "", "attach", "--room", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "HANDOFF") {
		t.Fatalf("invented handoff: %s", out)
	}
	posts, _ := svc.Rooms.Messages(rec.ID)
	if len(posts) != 1 || posts[0].Text != "An external orchestrator took the root seat" {
		t.Fatalf("notices: %+v", posts)
	}
}

func TestAttachPreservesLargeHandoffs(t *testing.T) {
	for _, written := range []bool{true, false} {
		t.Run(fmt.Sprint(written), func(t *testing.T) {
			clearStamps(t)
			svc := service(t)
			rec, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"})
			if err != nil {
				t.Fatal(err)
			}
			info, _, err := svc.Sessions.StartRoot(rec, "SLEEP:0 RESPOND: "+strings.Repeat("Initial work ", 800), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.Sessions.Wait(context.Background(), info.ID, 1); err != nil {
				t.Fatal(err)
			}
			path, _ := svc.Sessions.Store.NormalizedPath(info.ID)
			var transcript bytes.Buffer
			for range 6 {
				event, _ := json.Marshal(workeroutput.Event{Kind: "message", Role: "assistant", Text: strings.Repeat("Recorded work ", 300)})
				transcript.Write(event)
				transcript.WriteByte('\n')
			}
			if err := os.WriteFile(path, transcript.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			handoff := strings.Repeat("handoff 🌱 ", 1500)
			now := time.Now()
			svc.Sessions.TransferNow = func() time.Time { return now }
			svc.Sessions.TransferSleep = func(_ context.Context, delay time.Duration) error {
				now = now.Add(delay)
				if written {
					state, _ := svc.Sessions.TransferStatus(info.ID)
					return svc.Sessions.SubmitHandoff(context.Background(), info.ID, state.ID, handoff, "", "")
				}
				return nil
			}
			out, err := run(t, svc, "", "attach", "--room", "alpha", "--harness", "pi")
			if err != nil {
				t.Fatalf("attach failed for a large handoff: %v", err)
			}
			state, _ := svc.Sessions.TransferStatus(info.ID)
			full := state.Handoff
			if !strings.Contains(out, full) || !strings.Contains(full, "Review this source before acting") {
				t.Fatalf("handoff or source references missing from stdout: %d bytes", len(full))
			}
			if written && !strings.Contains(full, handoff) {
				t.Fatal("cooperative root attach lost its written handoff")
			}
			if !written && strings.Contains(full, "Recorded work Recorded work") {
				t.Fatal("root attach copied transcript excerpts")
			}
			posts, _ := svc.Rooms.Messages(rec.ID)
			var note room.Message
			for _, p := range posts {
				if p.Kind == "session" && p.Notice.Kind == "seat" {
					note = p
				}
			}
			if len(note.Text) > room.MaxMessageBytes {
				t.Fatalf("seat note needs %d bytes", len(note.Text))
			}
			if !written {
				if len(note.Attachments) != 0 {
					t.Fatal("references-only fallback needs no attachment")
				}
				return
			}
			if len(note.Attachments) != 1 {
				t.Fatal("full written handoff attachment missing")
			}
			path, err = svc.Rooms.AttachmentPath(rec.ID, note.Attachments[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil || string(saved) != full {
				t.Fatalf("full handoff attachment differs: %v", err)
			}
		})
	}
}

// attach and detach ask the engine; the CLI process changes no seat itself.
func TestSeatCommandsCallTheEngine(t *testing.T) {
	clearStamps(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svc := notify.New(room.DefaultStore(), &subagent.Manager{Store: subagent.DefaultStore()})
	rec, err := svc.Rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	record := func(name string) websvc.Call {
		return func(_ context.Context, args []json.RawMessage) (any, error) {
			for _, raw := range args {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					return nil, err
				}
				got[name] = append(got[name], s)
			}
			return nil, nil
		}
	}
	enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{"AttachSeat": record("AttachSeat"), "ReturnSeat": record("ReturnSeat")})
	note := t.TempDir() + "/note.txt"
	if err := os.WriteFile(note, []byte("PR 12 waits for CI."), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"attach", "--room", "alpha", "--harness", "pi"}, {"detach", "--room", "alpha", "--handoff", note}} {
		if out, err := run(t, svc, "", args...); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	if a, r := got["AttachSeat"], got["ReturnSeat"]; len(a) != 2 || a[0] != rec.ID || a[1] != "pi" || len(r) != 2 || r[0] != rec.ID || r[1] != "PR 12 waits for CI." {
		t.Fatalf("calls = %q; want AttachSeat [room pi] and ReturnSeat [room note]", got)
	}
	if cur, err := svc.Rooms.Resolve(rec.ID); err != nil || cur.Seat.External || !cur.Seat.Since.IsZero() {
		t.Fatalf("the CLI changed the seat itself: %+v, %v", cur.Seat, err)
	}
}
