package room

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestRoomLogFilters(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	st := room.DefaultStore()
	work := workRoom(t)
	var ids []string
	post := func(from, kind, to string) {
		t.Helper()
		m, err := st.Post(work.ID, room.PostInput{From: from, Kind: kind, To: []string{to}, Text: fmt.Sprintf("post %d", len(ids))})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	for i := 0; i < 35; i++ {
		post("work:lead", "message", "work")
	}
	post("shell:42", "completed", "work")
	post("run:42", "failed", "work")
	post("work:lead", "pr", "work")
	post("work:lead", "completed", "work")
	post("work:lead", "failed", "work")
	post("work:lead", "stopped", "work")
	post("work:child", "message", "work:lead")
	for _, tc := range []struct {
		name          string
		flags         []string
		count, hidden int
		first         string
	}{
		{"default", nil, 30, 8, ids[8]},
		{"all", []string{"--all"}, 42, 0, ids[0]},
		{"tail", []string{"--tail", "2"}, 2, 36, ids[39]},
		{"unlimited", []string{"--tail", "0"}, 38, 0, ids[0]},
		{"since", []string{"--since", ids[34]}, 3, 0, ids[38]},
		{"since filtered post", []string{"--since", ids[36]}, 3, 0, ids[38]},
		{"since last", []string{"--since", ids[41]}, 0, 0, ""},
		{"kind", []string{"--kind", "completed"}, 2, 0, ids[35]},
		{"repeat kind", []string{"--kind", "completed", "--kind", "failed"}, 4, 0, ids[35]},
		{"kind retains owner filter", []string{"--kind", "message", "--tail", "0"}, 35, 0, ids[0]},
		{"all explicit tail", []string{"--all", "--tail", "2"}, 2, 40, ids[40]},
		{"all explicit kind", []string{"--all", "--kind", "message"}, 36, 0, ids[0]},
		{"combined", []string{"--all", "--since", ids[34], "--kind", "failed", "--tail", "1"}, 1, 1, ids[39]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"log", "work", "--json"}, tc.flags...)
			out, err := execCmd(t, RoomCmd(), args...)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Posts  []room.Message
				Hidden int
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Posts) != tc.count || got.Hidden != tc.hidden {
				t.Fatalf("got %d posts, %d hidden: %s", len(got.Posts), got.Hidden, out)
			}
			if tc.count > 0 && got.Posts[0].ID != tc.first {
				t.Fatalf("first = %s, want %s", got.Posts[0].ID, tc.first)
			}
			text, err := execCmd(t, RoomCmd(), append([]string{"log", "work"}, tc.flags...)...)
			if err != nil {
				t.Fatal(err)
			}
			if tc.hidden > 0 && !strings.HasPrefix(text, fmt.Sprintf("%d older matching posts hidden; use --tail 0 to see them.\n", tc.hidden)) {
				t.Fatalf("notice missing: %s", text)
			}
			for _, id := range ids {
				want := false
				for _, m := range got.Posts {
					if m.ID == id {
						want = true
					}
				}
				if strings.Contains(text, id) != want {
					t.Fatalf("text and JSON differ for %s", id)
				}
			}
		})
	}
	for _, flags := range [][]string{{"--tail", "-1"}, {"--since", "missing"}, {"--tail", "invalid"}} {
		if _, err := execCmd(t, RoomCmd(), append([]string{"log", "work"}, flags...)...); err == nil {
			t.Fatalf("expected error for %v", flags)
		}
	}
}
