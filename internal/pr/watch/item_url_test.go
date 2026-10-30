package watch

import "testing"

func TestParseItemURL(t *testing.T) {
	for _, tc := range []struct {
		text, segment string
		want          Ref
		ok            bool
	}{
		{"https://github.com/o/r/pull/7", "pull", Ref{"github.com", "o/r", 7}, true},
		{"http://ghe.example:8443/o/r/issues/3", "issues", Ref{"ghe.example:8443", "o/r", 3}, true},
		{"https://github.com/o/r/issues/3", "pull", Ref{}, false},
		{"ftp://github.com/o/r/pull/7", "pull", Ref{}, false},
		{"https://user@github.com/o/r/pull/7", "pull", Ref{}, false},
		{"https:///o/r/issues/3", "issues", Ref{}, false},
		{"https://github.com/o/r/pull/0", "pull", Ref{}, false},
	} {
		got, ok := parseItemURL(tc.text, tc.segment)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseItemURL(%q, %q) = %+v, %v; want %+v, %v", tc.text, tc.segment, got, ok, tc.want, tc.ok)
		}
	}
}
