package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestArchiveRetriesDoNotStarveLaterRecords(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0); INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','closed','',0)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		repo := fmt.Sprintf("repo%02d", i)
		if _, err := s.writer.Exec("INSERT INTO repos(repo,default_room) VALUES(?,'r')", repo); err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.Exec("INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES('s',?,'ref','sha',0,1)", repo); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1000, 0)
	first, err := s.ExpiredArchives(ctx, now)
	if err != nil || len(first) != 50 {
		t.Fatalf("first batch %d: %v", len(first), err)
	}
	for _, a := range first {
		if err := s.RetryArchive(ctx, a, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	next, err := s.ExpiredArchives(ctx, now)
	if err != nil || len(next) != 1 || next[0].Repo != "repo50" {
		t.Fatalf("next batch %v: %v", next, err)
	}
}
