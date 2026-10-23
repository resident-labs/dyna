package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

type fakeMergeControl struct {
	action        string
	args, without []string
	room          string
	err           error
	flaky         []string
}

func (f *fakeMergeControl) MergeControl(_ context.Context, room, action string, args, without, flaky []string) error {
	f.room, f.action, f.args, f.without, f.flaky = room, action, args, without, flaky
	return f.err
}
func (f *fakeMergeControl) MergeControlStatus(room string) (watch.MergeControlStatus, error) {
	f.room = room
	return watch.MergeControlStatus{RoomID: room, Paused: true, Reason: "batch failed"}, f.err
}

func TestMergeCommandsForwardControls(t *testing.T) {
	for _, tc := range []struct {
		argv          []string
		action        string
		args, without []string
		flaky         []string
	}{
		{[]string{"pause"}, "pause", nil, nil, nil},
		{[]string{"note", "Retry #17; TestQueue passes on main."}, "note", []string{"Retry #17; TestQueue passes on main."}, nil, nil},
		{[]string{"resume"}, "resume", nil, nil, nil},
		{[]string{"cancel"}, "cancel", nil, nil, nil},
		{[]string{"return", "17", "fix the conflict"}, "return", []string{"17", "fix the conflict"}, nil, nil},
		{[]string{"retry", "--without", "17", "--without", "19"}, "retry", nil, []string{"17", "19"}, nil},
		{[]string{"bisect"}, "bisect", nil, nil, nil},
		{[]string{"publish", "trial-1"}, "publish", []string{"trial-1"}, nil, nil},
		{[]string{"publish", "trial-1", "--flaky", "TestQueue: racy temp dir"}, "publish", []string{"trial-1"}, nil, []string{"TestQueue: racy temp dir"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			f := &fakeMergeControl{}
			cmd := mergeCommand(func(string) (mergeController, string, error) { return f, "room-id", nil })
			cmd.SetArgs(tc.argv)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if f.room != "room-id" || f.action != tc.action || !reflect.DeepEqual(f.args, tc.args) || !reflect.DeepEqual(f.without, tc.without) || !reflect.DeepEqual(f.flaky, tc.flaky) {
				t.Fatalf("forwarded control = %#v", f)
			}
		})
	}
}

func TestMergeStatusJSONAndServiceErrors(t *testing.T) {
	denied := errors.New("root authority required")
	for _, action := range []string{"status", "pause"} {
		f := &fakeMergeControl{}
		roomRef := ""
		cmd := mergeCommand(func(ref string) (mergeController, string, error) { roomRef = ref; return f, "stamped-room", nil })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{action})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if roomRef != "" || f.room != "stamped-room" {
			t.Fatalf("room ref=%q room=%q", roomRef, f.room)
		}
		if action == "status" {
			var got watch.MergeControlStatus
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.RoomID != "stamped-room" || !got.Paused || got.Reason != "batch failed" {
				t.Fatalf("status = %#v", got)
			}
		}
		f.err = denied
		if err := cmd.Execute(); !errors.Is(err, denied) {
			t.Fatalf("service error = %v", err)
		}
	}
}

func TestMergeArgumentValidation(t *testing.T) {
	for _, argv := range [][]string{{"note"}, {"note", "one", "two"}, {"status", "extra"}, {"pause", "extra"}, {"return", "17"}, {"publish"}, {"retry", "17"}, {"bisect", "extra"}} {
		cmd := mergeCommand(func(string) (mergeController, string, error) {
			t.Fatal("invalid command reached service")
			return nil, "", nil
		})
		cmd.SetArgs(argv)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", argv)
		}
	}
}

func TestMergeRoomResolutionError(t *testing.T) {
	want := errors.New("room unavailable")
	cmd := mergeCommand(func(ref string) (mergeController, string, error) {
		if ref != "human-room" {
			t.Fatalf("room ref = %q", ref)
		}
		return nil, "", want
	})
	cmd.SetArgs([]string{"status", "--room", "human-room"})
	if err := cmd.Execute(); !errors.Is(err, want) {
		t.Fatalf("room error = %v", err)
	}
}

// Command dispatch for every action is covered above. One retry crosses the
// real engine socket to verify room resolution, argument encoding, and errors.
func TestMergeCommandMutationsUseEngine(t *testing.T) {
	storetest.Isolate(t)
	rooms := room.DefaultStore()
	rm, err := rooms.Create(room.CreateInput{Name: "merge-root"})
	if err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_ROOM_ID", rm.ID)
	t.Setenv("DYNA_ROOM", rm.Name)
	t.Setenv("DYNA_SESSION", "ses_root")
	called := false
	sentinel := errors.New("engine handled merge control")
	calls, err := websvc.Bind([]websvc.Binding{{Name: "MergeControl", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
		Method: func(_ context.Context, id, action string, args, without, flaky []string) error {
			called = true
			if id != rm.ID || action != "retry" || len(args) != 0 || !reflect.DeepEqual(without, []string{"17", "19"}) || len(flaky) != 0 {
				t.Errorf("engine arguments = %q %q %v %v", id, action, args, without)
			}
			return sentinel
		}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	cmd := MergeCommand(Options{Service: func() *watch.Service { return s }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"retry", "--without", "17", "--without", "19"})
	if err := cmd.Execute(); !called || err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("engine called=%v, error=%v", called, err)
	}
	st, err := s.Store.Read()
	if err != nil || len(st.MergePauses) != 0 {
		t.Fatalf("CLI changed local state: %+v, %v", st, err)
	}
}

func TestPRMergeStatusUsesEngineFairnessOrder(t *testing.T) {
	storetest.Isolate(t)
	// The engine owes normal work a turn after three expedited grants. A new
	// CLI process has no grants; it must retain the engine response verbatim.
	want := []watch.MergeQueueItem{{ChangeID: "normal", State: "ready"}, {ChangeID: "expedited", State: "ready", GoalID: "g1", Expedited: true}}
	called := false
	calls, err := websvc.Bind([]websvc.Binding{{Name: "MergeStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: func(context.Context) ([]watch.MergeQueueItem, error) { called = true; return want, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	cmd := Command(Options{Service: func() *watch.Service { t.Fatal("merge-status read local counters"); return nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"merge-status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got []watch.MergeQueueItem
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !called || !reflect.DeepEqual(got, want) {
		t.Fatalf("called=%v, rows=%+v", called, got)
	}
}
