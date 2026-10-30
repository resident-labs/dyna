package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
)

func TestApprovedCommentRecordsItsID(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'https://github.com/owner/repo/pull/7#issuecomment-4242'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, fake)
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "comment-room"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	raw, err := json.Marshal(approvals.GHPayload{Argv: []string{"pr", "comment", "https://github.com/owner/repo/pull/7", "--body", "hi"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const requester = "ses_20261029-000001-aaaaaaaa"
	var claimed []int64
	var session string
	claim := func(s string, id int64) error {
		session = s
		claimed = append(claimed, id)
		return nil
	}
	_, err = ghApprovalExecutor(rooms, claim)(context.Background(), approvals.Request{Room: rec.ID, Requester: requester, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0] != 4242 || session != requester {
		t.Fatalf("claimed %v for %q, want comment 4242 for %q", claimed, session, requester)
	}
}
