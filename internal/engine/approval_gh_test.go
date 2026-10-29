package engine

import (
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
)

func TestBindApprovalFilesRequiresSnapshot(t *testing.T) {
	_, _, err := bindApprovalFiles(approvals.GHPayload{Argv: []string{"pr", "create", "--body-file", "body.md"}})
	if err == nil || !strings.Contains(err.Error(), "no immutable snapshot") {
		t.Fatalf("error = %v", err)
	}
}

func TestBindApprovalFilesRestoresOriginalContents(t *testing.T) {
	payload := approvals.GHPayload{
		Argv: []string{"pr", "create", "--body-file", "body.md", "--body-file=review.md"},
		Files: []approvals.FileSnapshot{
			{Path: "body.md", Data: []byte("approved body")},
			{Path: "review.md", Data: []byte("approved review")},
		},
	}
	argv, cleanup, err := bindApprovalFiles(payload)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	body, err := os.ReadFile(argv[3])
	if err != nil || string(body) != "approved body" {
		t.Fatalf("body = %q, %v", body, err)
	}
	path, ok := strings.CutPrefix(argv[4], "--body-file=")
	if !ok {
		t.Fatalf("argument = %q", argv[4])
	}
	review, err := os.ReadFile(path)
	if err != nil || string(review) != "approved review" {
		t.Fatalf("review = %q, %v", review, err)
	}
}
