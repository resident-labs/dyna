package roomsvc

import (
	"reflect"
	"testing"
)

func TestFileCodeSpanPreservesFollowingRecipient(t *testing.T) {
	// This is the text inserted when the composer selects a filename with
	// a backtick, then a session. Mentions inside that file stay quoted.
	draft := "Review ``web docs/odd`file.txt`` `@other` @lead"
	got := ParseDraft(draft, "alpha", []string{"lead", "other"})
	if !reflect.DeepEqual(got.Mentions, []string{"@lead"}) || got.Default || got.Text != draft {
		t.Fatalf("ParseDraft(%q) = %+v", draft, got)
	}
}
