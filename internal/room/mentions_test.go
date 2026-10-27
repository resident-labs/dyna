package room

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitLeadingMentions(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		mentions []string
		rest     []string
	}{
		{[]string{"@lead", "hello", "there"}, []string{"@lead"}, []string{"hello", "there"}},
		{[]string{"@a", "@b", "--", "@c is text"}, []string{"@a", "@b"}, []string{"@c is text"}},
		{[]string{"no", "mention"}, nil, []string{"no", "mention"}},
		{[]string{"@lead", "", "hello"}, []string{"@lead"}, []string{"", "hello"}},
		{[]string{"@decoder,", "fix"}, []string{"@decoder,"}, []string{"fix"}},
	} {
		mentions, rest := SplitLeadingMentions(tc.args)
		if !reflect.DeepEqual(mentions, tc.mentions) || !reflect.DeepEqual(rest, tc.rest) {
			t.Fatalf("SplitLeadingMentions(%q) = %q, %q", tc.args, mentions, rest)
		}
		if !hasEmptyArg(tc.args) {
			got, text := SplitLeadingMentionText(strings.Join(tc.args, " "))
			if !reflect.DeepEqual(got, tc.mentions) || text != strings.Join(tc.rest, " ") {
				t.Fatalf("SplitLeadingMentionText(%q) = %q, %q", strings.Join(tc.args, " "), got, text)
			}
		}
	}
}

func hasEmptyArg(args []string) bool {
	for _, arg := range args {
		if arg == "" {
			return true
		}
	}
	return false
}
