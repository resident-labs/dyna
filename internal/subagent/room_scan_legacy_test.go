package subagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
)

func writeRoomScanFixture(t *testing.T, e *testEnv, id string, raw []byte) string {
	t.Helper()
	recordtest.WriteRaw(t, e.m.Store.Root, id, raw)
	return recordtest.Path(t, e.m.Store.Root, id)
}
func TestRoomControlsIgnoreLegacyUnassignedHistory(t *testing.T) {
	e := roomControlEnv(t)
	idleAt := time.Now().Add(-testIdleAfter - time.Minute)
	valid := Info{Meta: Meta{
		ID: NewID(), Name: "current", RoomID: e.room.ID, Room: e.room.Name,
		Profile: "mock", Depth: 1, Created: idleAt, LastActivity: idleAt, Finished: idleAt,
	}, Status: StatusIdle}
	data, err := json.Marshal(valid.Meta)
	if err != nil {
		t.Fatal(err)
	}
	writeRoomScanFixture(t, e, valid.ID, data)
	legacy := NewID()
	raw := []byte(`{"id":"` + legacy + `","historyOnly":true,"profile":"old","harnessSession":"preserved-native","turns":3}`)
	path := writeRoomScanFixture(t, e, legacy, raw)
	history := filepath.Join(filepath.Dir(path), "old-history.txt")
	if err = os.WriteFile(history, []byte("preserved old transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	// A correctly stamped session in another room is also excluded.
	other, err := e.m.RoomStore().Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := NewID()
	data, _ = json.Marshal(Meta{ID: foreign, RoomID: other.ID, Room: other.Name, Created: time.Now(), LastActivity: time.Now()})
	writeRoomScanFixture(t, e, foreign, data)
	state, err := e.m.RoomSessionControls(valid.RoomID)
	if err != nil || state.Dormant != 1 {
		t.Fatalf("status: %+v %v", state, err)
	}
	for _, paused := range []bool{true, false} {
		var result RoomSessionMutation
		if paused {
			result, err = e.m.PauseRoomSessions(e.ctx, valid.RoomID)
		} else {
			result, err = e.m.ResumeRoomSessions(e.ctx, valid.RoomID)
		}
		if err != nil || result.State.Paused != paused || len(result.Results) != 1 || result.Results[0].Session != valid.ID {
			t.Fatalf("pause=%t: %+v %v", paused, result, err)
		}
	}
	e.doneGoals()
	result, err := closeIdle(e.m, e.ctx, valid.RoomID)
	if err != nil || result.Succeeded != 1 || len(result.Results) != 1 || result.Results[0].Session != valid.ID {
		t.Fatalf("clear: %+v %v", result, err)
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("legacy record was changed: %s %v", current, err)
	}
	if current, err := os.ReadFile(history); err != nil || string(current) != "preserved old transcript" {
		t.Fatal("legacy history was changed", err)
	}
	if _, err = e.m.Store.ReadMeta(legacy); err != nil {
		t.Fatalf("strict ReadMeta changed legacy semantics: %v", err)
	}
	foreignMeta, err := e.m.Store.ReadMeta(foreign)
	if err != nil || !foreignMeta.Closed.IsZero() {
		t.Fatal("foreign session changed", err)
	}
}
func TestRoomControlsRefuseMalformedAndPartialRoomEvidence(t *testing.T) {
	for _, name := range []string{"malformed", "null-record", "empty-record", "mismatched-id", "missing-room", "missing-room-id", "empty-stamp", "null-stamp", "invalid-field"} {
		t.Run(name, func(t *testing.T) {
			e := roomControlEnv(t)
			valid, _ := e.start("SLEEP:1 RESPOND: current")
			e.wait(valid.ID, 1)
			e.waitStatus(valid.ID, StatusIdle)
			e.m.Store.UpdateMeta(valid.ID, func(meta *Meta) {
				meta.LastActivity, meta.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				meta.Status = StatusDormant
			})
			id := NewID()
			fields := map[string]any{"id": id, "roomId": valid.RoomID, "room": valid.Room, "profile": "mock"}
			switch name {
			case "mismatched-id":
				fields["id"] = NewID()
			case "missing-room":
				delete(fields, "room")
			case "missing-room-id":
				delete(fields, "roomId")
			case "empty-stamp":
				fields["roomId"], fields["room"] = "", ""
			case "null-stamp":
				fields["roomId"], fields["room"] = nil, nil
			case "invalid-field":
				fields["profile"] = 17
			}
			raw, _ := json.Marshal(fields)
			switch name {
			case "malformed":
				raw = []byte(`{`)
			case "null-record":
				raw = []byte(`null`)
			case "empty-record":
				raw = []byte(`{}`)
			}
			path := writeRoomScanFixture(t, e, id, raw)
			if name == "missing-room" || name == "missing-room-id" {
				if _, err := e.m.Store.ReadMeta(id); !errors.Is(err, ErrNoRoom) {
					t.Fatalf("partial stamp must retain ErrNoRoom: %v", err)
				}
			}
			if _, err := e.m.RoomSessionControls(valid.RoomID); err == nil {
				t.Fatal("status ignored damaged metadata")
			}
			if _, err := e.m.PauseRoomSessions(e.ctx, valid.RoomID); err == nil {
				t.Fatal("pause silently ignored damaged metadata")
			}
			if _, err := closeIdle(e.m, e.ctx, valid.RoomID); err == nil {
				t.Fatal("clear ignored damaged evidence")
			}
			meta, err := e.m.Store.ReadMeta(valid.ID)
			if err != nil || !meta.Closed.IsZero() {
				t.Fatal("clear closed current work despite damaged evidence", err)
			}
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, raw) {
				t.Fatal("damaged record was rewritten", err)
			}
		})
	}
}

// Open-session controls skip closed history before parsing it, so damaged
// closed records cannot block them and do not cost a full read.
func TestRoomControlsSkipClosedHistory(t *testing.T) {
	e := roomControlEnv(t)
	closed := NewID()
	raw := []byte(`{"id":"` + closed + `","roomId":"` + e.room.ID + `","room":"","closed":"2026-01-01T00:00:00Z"}`)
	writeRoomScanFixture(t, e, closed, raw)
	if _, err := e.m.RoomSessionControls(e.room.ID); err != nil {
		t.Fatalf("closed history blocked room controls: %v", err)
	}
}

func TestOpenMetaRechecksClosureAfterFullRead(t *testing.T) {
	e := roomControlEnv(t)
	info := Meta{ID: NewID(), Name: "closing", RoomID: e.room.ID, Room: e.room.Name, Profile: "mock", Depth: 1, Created: time.Now(), LastActivity: time.Now()}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	writeRoomScanFixture(t, e, info.ID, data)
	beforeRoomMetaRead = func(id string) {
		if id == info.ID {
			if _, err := e.m.Store.UpdateMeta(id, func(m *Meta) { m.Closed = time.Now() }); err != nil {
				t.Error(err)
			}
		}
	}
	defer func() { beforeRoomMetaRead = func(string) {} }()
	all, err := e.m.Store.ListOpenMeta()
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range all {
		if meta.ID == info.ID {
			t.Fatalf("a session that closed during the scan is listed as open: %+v", meta)
		}
	}
}
