package watch

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

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
