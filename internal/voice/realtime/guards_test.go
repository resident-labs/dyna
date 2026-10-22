package realtime

import (
	"strings"
	"testing"
	"time"
)

func TestGuardReplyMatchesOnlyReservedTags(t *testing.T) {
	for _, bad := range []string{"<background_work_progress>CI is green</background_work_progress>", "Done. <permission_request id=1>", "</restored_context>"} {
		if guardReply(bad) == "" {
			t.Errorf("%q passed the guard", bad)
		}
	}
	for _, fine := range []string{"CI is green.", "The tag <b>bold</b> is fine.", "a <background_work_progressive> word"} {
		if guardReply(fine) != "" {
			t.Errorf("%q was guarded", fine)
		}
	}
}

// A model reply that writes a reserved tag is not recorded, and once it
// ends Voice is asked to answer again. A correction that does it again is
// not corrected a second time; an ordinary reply is recorded.
func TestCallCorrectsAReplyThatWritesAReservedTag(t *testing.T) {
	cf := startCall(t)
	cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
	cf.f.reply <- `{"type":"response.output_audio_transcript.done","response_id":"r1","transcript":"<background_work_progress>done</background_work_progress>"}`
	cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"completed"}}`
	item := cf.serverGets(t, "conversation.item.create")
	content, _ := field(item, "item", "content").([]any)
	if got, _ := field(content[0], "text").(string); !strings.Contains(got, "internal event format") {
		t.Fatalf("server got %q, want the correction", got)
	}
	cf.serverGets(t, "response.create")
	cf.f.reply <- `{"type":"response.created","response":{"id":"r2"}}`
	cf.f.reply <- `{"type":"response.output_audio_transcript.done","response_id":"r2","transcript":"<background_work_progress>again</background_work_progress>"}`
	cf.f.reply <- `{"type":"response.done","response":{"id":"r2","status":"completed"}}`
	cf.mark(t) // no second correction
	cf.f.reply <- `{"type":"response.created","response":{"id":"r3"}}`
	cf.f.reply <- `{"type":"response.output_audio_transcript.done","response_id":"r3","transcript":"CI is green."}`
	select {
	case got := <-cf.lines:
		if got.owner || got.text != "CI is green." {
			t.Fatalf("line %+v, want only the ordinary reply", got)
		}
	case <-cf.f.got:
		t.Fatal("the server got something for an ordinary reply")
	case <-time.After(waitLimit):
		t.Fatal("the ordinary reply was not recorded")
	}
}

// A reply the owner stopped is not corrected: not when the cancel comes
// after the guarded words, and not when its late words come after the
// owner's next turn.
func TestCallNeverCorrectsACancelledReply(t *testing.T) {
	tagged := func(id string) string {
		return `{"type":"response.output_audio_transcript.done","response_id":"` + id + `","transcript":"<background_work_progress>x</background_work_progress>"}`
	}
	t.Run("cancel after the words", func(t *testing.T) {
		cf := startCall(t)
		cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
		cf.f.reply <- tagged("r1")
		cf.sync(t)
		cf.call.FromPage(PageMessage{Type: "cancel"})
		cf.serverGets(t, "response.cancel")
		cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"cancelled"}}`
		cf.sync(t)
		cf.mark(t) // no correction
	})
	t.Run("late words after a new turn", func(t *testing.T) {
		cf := startCall(t)
		cf.f.reply <- `{"type":"response.created","response":{"id":"r1"}}`
		cf.sync(t)
		cf.call.FromPage(PageMessage{Type: "cancel"})
		cf.serverGets(t, "response.cancel")
		cf.f.reply <- `{"type":"input_audio_buffer.speech_started"}`
		cf.serverGets(t, "response.cancel") // the owner's speech stops r1 again
		cf.f.reply <- `{"type":"input_audio_buffer.committed"}`
		cf.f.reply <- tagged("r1") // late words of the cancelled reply
		cf.f.reply <- `{"type":"response.done","response":{"id":"r1","status":"cancelled"}}`
		if create := cf.serverGets(t, "response.create"); create["response"] != nil {
			t.Fatalf("server got %v, want only the owner's reply", create)
		}
		cf.f.reply <- `{"type":"response.created","response":{"id":"r2"}}`
		cf.f.reply <- `{"type":"response.done","response":{"id":"r2","status":"completed"}}`
		cf.sync(t)
		cf.mark(t) // no correction after the owner's reply either
	})
}
