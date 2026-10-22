package watch

import "testing"

func TestKeepLinesKeepsMarkdownStructure(t *testing.T) {
	got := keepLines("# Review  \r\n\r\n\r\n\r\n## Summary\n- one \n- two\n\n")
	want := "# Review\n\n## Summary\n- one\n- two"
	if got != want {
		t.Fatalf("keepLines = %q, want %q", got, want)
	}
}
