package watch

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHeadChunkDeadlineDoesNotBlockLaterChunk(t *testing.T) {
	old := withHeadChunkTimeout
	defer func() { withHeadChunkTimeout = old }()
	calls := 0
	withHeadChunkTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		calls++
		if calls <= 3 {
			return context.WithDeadline(ctx, time.Unix(0, 0))
		}
		return context.WithTimeout(ctx, d)
	}
	g := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{"defaultBranchRef":{"name":"main"},"p21":{"number":21,"state":"OPEN","headRefOid":"`+strings.Repeat("a", 40)+`"}}}}`)
		return nil
	}}
	var records []*Record
	for i := 1; i <= 21; i++ {
		records = append(records, &Record{Key: fmt.Sprint(i), Host: "github.com", Repo: "owner/repo", Number: i})
	}
	heads, _, err := g.readHeads(t.Context(), records)
	if err == nil || heads["1"].Err == nil || heads["21"].Err != nil || heads["21"].Pull.Number != 21 || calls != 4 {
		t.Fatalf("calls=%d first=%+v last=%+v err=%v", calls, heads["1"], heads["21"], err)
	}
}

func TestHeadChunksSplitFailedBatch(t *testing.T) {
	var sizes []int
	re := regexp.MustCompile(`p([0-9]+):pullRequest`)
	g := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
		query := cmd.Args[len(cmd.Args)-1]
		matches := re.FindAllStringSubmatch(query, -1)
		sizes = append(sizes, len(matches))
		if len(matches) > 2 {
			fmt.Fprint(cmd.Stdout, "HTTP/2.0 504 Gateway Timeout\r\n\r\n")
			return nil
		}
		fmt.Fprint(cmd.Stdout, `HTTP/2.0 200 OK`+"\r\n\r\n"+`{"data":{"r0":{"defaultBranchRef":{"name":"main"}`)
		for _, m := range matches {
			n, _ := strconv.Atoi(m[1])
			fmt.Fprintf(cmd.Stdout, `,"p%d":{"number":%d,"state":"OPEN","headRefOid":"%s"}`, n, n, strings.Repeat("a", 40))
		}
		fmt.Fprint(cmd.Stdout, "}}}")
		return nil
	}}
	var records []*Record
	for i := 1; i <= 4; i++ {
		records = append(records, &Record{Key: fmt.Sprint(i), Host: "github.com", Repo: "owner/repo", Number: i})
	}
	heads, _, err := g.readHeads(t.Context(), records)
	if err != nil || len(heads) != 4 {
		t.Fatalf("heads=%v err=%v", heads, err)
	}
	for _, h := range heads {
		if h.Err != nil {
			t.Fatal(h.Err)
		}
	}
	if fmt.Sprint(sizes) != "[4 2 2]" {
		t.Fatalf("batch sizes: %v", sizes)
	}
}
