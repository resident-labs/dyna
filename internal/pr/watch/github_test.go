package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestKeepLinesKeepsMarkdownStructure(t *testing.T) {
	got := keepLines("# Review  \r\n\r\n\r\n\r\n## Summary\n- one \n- two\n\n")
	want := "# Review\n\n## Summary\n- one\n- two"
	if got != want {
		t.Fatalf("keepLines = %q, want %q", got, want)
	}
}

func TestCommitStatusHistoryPreservesExternalFailure(t *testing.T) {
	head := strings.Repeat("a", 40)
	path := "repos/owner/repo/commits/" + head + "/statuses?per_page=100"
	local := Status{ID: 3, Context: "go-test", State: "success", Description: localStatusPrefix + "node"}
	external := Status{ID: 1, Context: "go-test", State: "failure"}
	pages := 0
	gh := GitHub{run: func(_ context.Context, cmd *exec.Cmd) error {
		endpoint := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
			}
		}
		headers := "HTTP/2.0 200 OK\r\n"
		var body any
		switch endpoint {
		case "repos/owner/repo/commits/" + head + "/status?per_page=100":
			body = map[string]any{"statuses": []Status{local}}
		case path:
			pages++
			body = []Status{local}
			headers += "Link: <https://api.github.com/" + path + "&page=2>; rel=\"next\"\r\n"
		case path + "&page=2":
			pages++
			body = []Status{external}
		default:
			return fmt.Errorf("unexpected status endpoint %s", endpoint)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.Stdout, "%s\r\n%s", headers, raw)
		return err
	}}
	snap := Snapshot{}
	if err := gh.readCommitStatuses(context.Background(), Ref{Host: "github.com", Repo: "owner/repo"}, head, &snap); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(snap.Statuses) != 2 {
		t.Fatalf("lost status history: pages=%d statuses=%+v", pages, snap.Statuses)
	}
	if ci, _, _, _ := snap.CIStatus("go-test"); ci != "failed" {
		t.Fatalf("local status hid hosted failure with local mode off: %s", ci)
	}
	summaries := snap.CheckSummaries(Ref{Host: "github.com", Repo: "owner/repo"})
	if len(summaries) != 2 {
		t.Fatalf("hosted and local statuses were combined: %+v", summaries)
	}
	snap.localCI = &registeredCIObservation{Checks: []CheckSummary{{Name: "go-test", Node: "node", State: "success"}}}
	summaries = snap.CheckSummaries(Ref{Host: "github.com", Repo: "owner/repo"})
	if len(summaries) != 2 {
		t.Fatalf("local overlay hid hosted status: %+v", summaries)
	}
	snap.localCI = nil
	snap.Statuses = append(snap.Statuses, Status{ID: 4, Context: "go-test", State: "success"})
	if ci, _, _, _ := snap.CIStatus("go-test"); ci != "passed" {
		t.Fatalf("old hosted failure blocked its successful replacement: %s", ci)
	}
}
