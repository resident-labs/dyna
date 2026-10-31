package watch

import (
	"context"
	"errors"
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
		if calls != 2 {
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

// A head request slower than 3 seconds but within 10 succeeds on the injected clock.
func TestHeadChunkWaitsLongerThanThreeSeconds(t *testing.T) {
	old := withHeadChunkTimeout
	defer func() { withHeadChunkTimeout = old }()
	start := time.Now()
	withHeadChunkTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, start.Add(d))
	}
	const latency = 5 * time.Second
	g := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
		// Simulated server latency: the request fails only when its deadline comes first.
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(start.Add(latency)) {
			return context.DeadlineExceeded
		}
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{"defaultBranchRef":{"name":"main"},"p1":{"number":1,"state":"OPEN","headRefOid":"`+strings.Repeat("a", 40)+`"}}}}`)
		return nil
	}}
	heads, _, err := g.readHeads(t.Context(), []*Record{{Key: "1", Host: "github.com", Repo: "owner/repo", Number: 1}})
	if err != nil || heads["1"].Err != nil || heads["1"].Pull.Number != 1 {
		t.Fatalf("head=%+v err=%v", heads["1"], err)
	}
}

// Stalled chunks and their retries must leave time for the chunks after them.
func TestHeadChunkRetriesDoNotStarveLaterChunks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records int
	}{{"one stalled chunk", 21}, {"two stalled chunks", 41}} {
		t.Run(tc.name, func(t *testing.T) {
			old := withHeadChunkTimeout
			defer func() { withHeadChunkTimeout = old }()
			start := time.Now()
			var elapsed time.Duration
			withHeadChunkTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
				return context.WithDeadline(ctx, start.Add(elapsed+d))
			}
			parent, cancel := context.WithDeadline(t.Context(), start.Add(15*time.Second))
			defer cancel()
			last := fmt.Sprintf("p%d:", tc.records)
			g := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
				// Simulated GitHub: every head but the last stalls for a minute; the last answers in one second.
				latency := 60 * time.Second
				if strings.Contains(cmd.Args[len(cmd.Args)-1], last) {
					latency = time.Second
				}
				if deadline, ok := ctx.Deadline(); ok && deadline.Before(start.Add(elapsed+latency)) {
					elapsed = deadline.Sub(start)
					return context.DeadlineExceeded
				}
				elapsed += latency
				fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{"defaultBranchRef":{"name":"main"},"`+last[:len(last)-1]+`":{"number":`+fmt.Sprint(tc.records)+`,"state":"OPEN","headRefOid":"`+strings.Repeat("a", 40)+`"}}}}`)
				return nil
			}}
			var records []*Record
			for i := 1; i <= tc.records; i++ {
				records = append(records, &Record{Key: fmt.Sprint(i), Host: "github.com", Repo: "owner/repo", Number: i})
			}
			heads, _, _ := g.readHeads(parent, records)
			key := fmt.Sprint(tc.records)
			if heads[key].Err != nil || heads[key].Pull.Number != tc.records {
				t.Fatalf("last head=%+v elapsed=%v", heads[key], elapsed)
			}
		})
	}
}

// A cancelled parent context still cancels the head request.
func TestHeadChunkKeepsParentCancellation(t *testing.T) {
	old := withHeadChunkTimeout
	defer func() { withHeadChunkTimeout = old }()
	withHeadChunkTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, d)
	}
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	g := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
		return ctx.Err()
	}}
	heads, _, err := g.readHeads(parent, []*Record{{Key: "1", Host: "github.com", Repo: "owner/repo", Number: 1}})
	if err == nil || !errors.Is(heads["1"].Err, context.Canceled) {
		t.Fatalf("head=%+v err=%v", heads["1"], err)
	}
}

func TestHeadDeadlinePerAccountRequest(t *testing.T) {
	old := withHeadChunkTimeout
	defer func() { withHeadChunkTimeout = old }()
	calls := 0
	withHeadChunkTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		calls++
		if calls != 2 {
			return context.WithDeadline(ctx, time.Unix(0, 0))
		}
		return context.WithTimeout(ctx, d)
	}
	g := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{"defaultBranchRef":{"name":"main"},"p2":{"number":2,"state":"OPEN","headRefOid":"`+strings.Repeat("a", 40)+`"}}}}`)
		return nil
	}}
	records := []*Record{
		{Key: "1", Host: "a.example", Repo: "owner/repo", Number: 1},
		{Key: "2", Host: "b.example", Repo: "owner/repo", Number: 2},
	}
	heads, _, err := g.readHeads(t.Context(), records)
	if err == nil || heads["1"].Err == nil || heads["2"].Err != nil || heads["2"].Pull.Number != 2 || calls != 3 {
		t.Fatalf("calls=%d first=%+v second=%+v err=%v", calls, heads["1"], heads["2"], err)
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
