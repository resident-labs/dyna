package ghclient

import (
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
)

func TestAttributeApprovalEndsCommentAndReviewBodies(t *testing.T) {
	line := "Posted by an agent."
	tests := []struct {
		name string
		argv []string
		path string
	}{
		{name: "issue comment", argv: []string{"issue", "comment", "1", "--body", "Comment"}},
		{name: "pr review", argv: []string{"pr", "review", "1", "--approve", "-bReview"}},
		{name: "close comment", argv: []string{"issue", "close", "1", "--comment", "Closing"}},
		{name: "comment file", argv: []string{"pr", "comment", "1", "--body-file", "body.md"}, path: "body.md"},
		{name: "api field", argv: []string{"api", "repos/o/r/issues/1/comments", "-F", "body=@body.md"}, path: "body.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := approvals.GHPayload{Argv: tt.argv}
			if tt.path != "" {
				payload.Files = []approvals.FileSnapshot{{Path: tt.path, Data: []byte("File body")}}
			}
			got := AttributeApproval(payload, line)
			preview := DisplayApprovalBody(got)
			if !strings.HasSuffix(preview, line) || strings.Count(preview, line) != 1 {
				t.Fatalf("preview = %q", preview)
			}
			if got = AttributeApproval(got, line); strings.Count(DisplayApprovalBody(got), line) != 1 {
				t.Fatal("attribution was appended twice")
			}
		})
	}
}

func TestAttributeApprovalUpdatesAPIInputBody(t *testing.T) {
	payload := approvals.GHPayload{Argv: []string{"api", "repos/o/r/pulls/1/reviews", "--input", "review.json"}, Files: []approvals.FileSnapshot{{Path: "review.json", Data: []byte("{\"event\":\"COMMENT\",\"body\":\"Review\"}")}}}
	got := AttributeApproval(payload, "Posted by an agent.")
	preview := DisplayApprovalBody(got)
	if !strings.Contains(preview, "\"body\":\"Review\\n\\nPosted by an agent.\"") {
		t.Fatalf("preview = %q", preview)
	}
}
