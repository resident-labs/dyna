package room

import (
	"os"
	"testing"
)

// The root seat reads its wakes, its turn input and its quiet sections from the
// stored delivery rows. Each case is one decision the address rules made before
// the rows existed: the same posts wake the seat, the same posts are quiet
// context in the same section, and the seat's own posts and the Voice call's
// lines are never its context.
func TestSeatRowsCarryTheRootDecisions(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	const roomName = "work"
	root := roomName + ":" + RootAgent
	voice := roomName + ":" + VoiceAgent
	for _, tc := range []struct {
		name string
		in   PostInput
		// event posts a system event instead of a plain post, as the engine
		// does for its own notes.
		event Event
		to    []string
		// row is false for a post that is not the seat's context at all.
		row      bool
		mode     DeliveryMode
		category string
		input    bool
		// ownerReport and producer describe a typed event's post.
		ownerReport bool
		producer    EventProducer
	}{
		{
			name: "human request",
			in:   PostInput{Wake: true, From: roomName, Human: true, Kind: "message", To: []string{roomName}, Text: "Fix the failing test"},
			row:  true, mode: DeliveryWake, input: true,
		},
		{
			name: "voice relay",
			in:   PostInput{Wake: true, From: voice, Kind: "message", Source: SourceVoice, To: []string{roomName}, Text: "Fix the failing test"},
			row:  true, mode: DeliveryWake, input: true,
		},
		{
			name: "alert",
			in:   PostInput{Wake: true, From: roomName + ":lead", Kind: "service-alert", To: []string{root}, Text: "Main is red"},
			row:  true, mode: DeliveryWake, input: true,
		},
		{
			name: "needs-root request",
			in:   PostInput{Wake: true, NeedsRoot: true, From: roomName + ":lead", Kind: "message", To: []string{roomName}, Text: "Choose the merge order"},
			row:  true, mode: DeliveryWake, input: true,
		},
		{
			name:        "completion owed to the owner",
			event:       TurnEvent{Session: roomName + ":lead", SessionID: "ses_lead", Turn: 1, State: TurnCompleted, Summary: "Report shipped"},
			to:          []string{roomName},
			ownerReport: true,
			row:         true, mode: DeliveryQuiet, category: RootQuietPost,
		},
		{
			name:        "failed report",
			event:       TurnEvent{Session: roomName + ":lead", SessionID: "ses_lead", Turn: 1, State: TurnFailed, Summary: "The turn failed", Error: "the provider refused the request"},
			to:          []string{roomName},
			ownerReport: true,
			row:         true, mode: DeliveryWake, input: true,
		},
		{
			name:        "shell result owed to the owner",
			event:       ShellEvent{ShellID: "sh_20261023-120000-00000001", Label: "make", State: ShellCompleted, ExitCode: 0},
			to:          []string{roomName},
			ownerReport: true,
			producer:    ProducerShell,
			row:         true, mode: DeliveryWake, input: true,
		},
		{
			name: "owner DM to an agent",
			in:   PostInput{Wake: true, From: roomName, Human: true, Kind: "message", To: []string{roomName + ":lead"}, Via: []string{"@lead"}, Text: "Approve the rename"},
			row:  true, mode: DeliveryQuiet, category: RootOwnerDM,
		},
		{
			name: "agent post to root",
			in:   PostInput{Wake: true, From: roomName + ":lead", Kind: "message", To: []string{roomName}, Text: "Paused the queue"},
			row:  true, mode: DeliveryQuiet, category: RootQuietPost,
		},
		{
			name: "owner shell post to the root session",
			in:   PostInput{From: roomName, Kind: "message", To: []string{root}, Text: "Yes, download it"},
			row:  true, mode: DeliveryQuiet, input: true,
		},
		{
			name:  "passive engine note",
			event: SessionEvent{Type: SessionInstructions, Session: roomName + ":lead", Detail: "Updated room rules"},
			to:    []string{root},
			row:   true, mode: DeliveryQuiet, input: true,
		},
		{
			name: "root's own post",
			in:   PostInput{Wake: true, From: root, Kind: "message", To: []string{roomName}, Text: "On it"},
		},
		{
			name: "voice call line",
			in:   PostInput{From: voice, Kind: "voice", Source: SourceVoice, To: []string{roomName}, Text: "On it."},
		},
		{
			name: "post between agents",
			in:   PostInput{Wake: true, From: roomName + ":a", Kind: "service-alert", To: []string{roomName + ":b"}, Text: "Heads up"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			var err error
			if tc.event != nil {
				msg, err = s.PostEventInput(PostEventInput{RoomID: rec.ID, Event: tc.event, To: tc.to, OwnerReport: tc.ownerReport, Producer: tc.producer})
			} else {
				msg, err = s.Post(rec.ID, tc.in)
			}
			if err != nil {
				t.Fatal(err)
			}
			seat, ok, err := s.SeatDelivery(rec.ID, msg.ID)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.row {
				t.Fatalf("seat row %v, want %v (post %+v)", ok, tc.row, msg)
			}
			if !tc.row {
				return
			}
			if seat.SessionID != SeatSessionID(rec.ID) || seat.Mode != tc.mode || seat.Category != tc.category || seat.RootInput() != tc.input {
				t.Fatalf("seat row = %+v, want mode %q, category %q, input %v", seat, tc.mode, tc.category, tc.input)
			}
		})
	}
}

// A post whose append fails leaves no rows behind: the rows go in before the
// line, so the store takes them out again when the line does not follow.
func TestFailedAppendLeavesNoSeatRows(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := s.Post(rec.ID, PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Keep the queue paused"})
	if err != nil {
		t.Fatal(err)
	}
	if seat, ok, err := s.SeatDelivery(rec.ID, quiet.ID); err != nil || !ok {
		t.Fatalf("the stored post has no seat row: %+v, %v, %v", seat, ok, err)
	}
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	seatRows := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM post_deliveries WHERE room_id=?", rec.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := seatRows()
	// A directory where the log file belongs fails the append after the rows
	// are written, whoever runs the test.
	path, err := s.messagesPath(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(rec.ID, PostInput{Wake: true, NeedsRoot: true, From: "work:lead", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"}); err == nil {
		t.Fatal("the post was stored with no log to write to")
	}
	if after := seatRows(); after != before {
		t.Fatalf("a failed append left %d more rows behind", after-before)
	}
}

// A row whose post is not in the log, as a crash between the row write and the
// append leaves it, is skipped instead of deciding anything for the seat.
func TestNewestSeatDeliverySkipsARowWithoutItsPost(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	wake, err := s.Post(rec.ID, PostInput{Wake: true, From: "work:lead", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	// The rows a crash left behind: newer than the stored post, with no post.
	if _, err := db.Exec(`INSERT INTO post_homes(room_id, post_id, ts, conversation, author_session_id) VALUES(?, 'msg_00000000000000fe', ?, 'room', '')`, rec.ID, wake.TS+1000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_deliveries(room_id, post_id, session_id, mode, category, state, updated_at) VALUES(?, 'msg_00000000000000fe', ?, 'quiet', '', 'queued', 1)`, rec.ID, SeatSessionID(rec.ID)); err != nil {
		t.Fatal(err)
	}
	if seat, ok, err := s.NewestSeatDelivery(rec.ID); err != nil || !ok || seat.PostID != wake.ID || seat.Mode != DeliveryWake {
		t.Fatalf("newest seat row = %+v, %v, %v; want the waking post %s", seat, ok, err, wake.ID)
	}
}

func TestNewestSeatDeliveryFollowsTheLog(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.NewestSeatDelivery(rec.ID); err != nil || ok {
		t.Fatalf("a room without seat rows reported one: %v, %v", ok, err)
	}
	quiet, err := s.Post(rec.ID, PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Keep the queue paused"})
	if err != nil {
		t.Fatal(err)
	}
	if seat, ok, err := s.NewestSeatDelivery(rec.ID); err != nil || !ok || seat.PostID != quiet.ID || seat.Mode != DeliveryQuiet {
		t.Fatalf("newest seat row = %+v, %v, %v", seat, ok, err)
	}
	wake, err := s.Post(rec.ID, PostInput{Wake: true, From: "work:lead", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"})
	if err != nil {
		t.Fatal(err)
	}
	if seat, ok, err := s.NewestSeatDelivery(rec.ID); err != nil || !ok || seat.PostID != wake.ID || seat.Mode != DeliveryWake {
		t.Fatalf("newest seat row = %+v, %v, %v", seat, ok, err)
	}
}
