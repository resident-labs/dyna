package gitsvc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitParserConflictsAndCaps(t *testing.T) {
	raw := "# branch.oid abc\x00# branch.head topic\x00# branch.upstream origin/main\x00# branch.ab +2 -3\x00u UU N... 100644 100644 100644 100644 a b c conflict name\x00? full\x00? partial"
	status := parseGitStatus([]byte(raw), true)
	if status.Ahead != 2 || status.Behind != 3 || len(status.Files) != 2 || !status.Files[0].Conflicted || status.Files[0].Status != "U" || status.Files[1].Path != "full" {
		t.Fatalf("parsed: %+v", status)
	}
	status = parseGitStatus([]byte("2 R. N... 100644 100644 100644 a b R100 new\x00old par"), true)
	if len(status.Files) != 0 {
		t.Fatalf("partial rename kept: %+v", status)
	}
	buffer := &CappedBuffer{Limit: 3}
	n, err := buffer.Write([]byte("abcde"))
	if err != nil || n != 5 || !buffer.Truncated || !bytes.Equal(buffer.Bytes(), []byte("abc")) {
		t.Fatalf("buffer: %+v %d %v", buffer, n, err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, _, err := runWorkspaceGit(ctx, t.TempDir(), 100, "status"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestCommitPRSubjects(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    int
	}{
		{"Feature (#123)", 123}, {"Merge pull request #456 from team/branch", 456}, {"Fix issue #12", 0}, {"No reference", 0},
	} {
		if got := commitPR(tc.subject); got != tc.want {
			t.Errorf("%q: got %d, want %d", tc.subject, got, tc.want)
		}
	}
}

func TestGitBlobCapStopsOutputCopy(t *testing.T) {
	buffer := &CappedBuffer{Limit: 3, stopAtLimit: true}
	n, err := buffer.Write([]byte("abcdef"))
	if n != 3 || !errors.Is(err, errOutputLimit) || !buffer.Truncated || buffer.String() != "abc" {
		t.Fatalf("bounded blob writer: n=%d err=%v data=%q truncated=%v", n, err, buffer.String(), buffer.Truncated)
	}
}
