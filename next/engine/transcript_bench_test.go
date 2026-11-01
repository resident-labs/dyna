package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// BenchmarkTranscriptStreaming uses eight independent sessions and fixed-size JSONL chunks.
func BenchmarkTranscriptStreaming(b *testing.B) {
	ctx := context.Background()
	st, err := store.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	s := New(st, fakeBoxes{}, fakeProfiles{}, time.Now)
	s.SetTranscriptDir(b.TempDir())
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "bench", RootProfile: "test"})
	if problem != nil {
		b.Fatal(problem)
	}
	const sessions = 8
	var callers [sessions]contract.Principal
	for i := range callers {
		session, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: fmt.Sprintf("worker%d", i), Profile: "test"})
		if problem != nil {
			b.Fatal(problem)
		}
		callers[i] = contract.Principal{Kind: contract.PrincipalSession, Session: session.Session}
	}
	chunk := []byte("{\"type\":\"message\",\"message\":{\"role\":\"assistant\",\"content\":\"streaming text\"}}\n")
	before := st.WriterTiming()
	beforeTranscript := s.TranscriptTiming()
	b.ResetTimer()
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var offset int64
			for n := i; n < b.N; n += sessions {
				response, problem := s.TranscriptAppend(ctx, callers[i], contract.TranscriptAppendRequest{Path: "stream.jsonl", Offset: offset, Bytes: chunk})
				if problem != nil {
					b.Error(problem)
					return
				}
				offset = response.AckedOffset
			}
		}(i)
	}
	wg.Wait()
	b.StopTimer()
	after := st.WriterTiming()
	afterTranscript := s.TranscriptTiming()
	if n := float64(count - count0); n > 0 {
		b.ReportMetric(float64(afterTranscript.WaitNS-beforeTranscript.WaitNS)/n, "lock-wait-ns/append")
		b.ReportMetric(float64(afterTranscript.SyncNS-beforeTranscript.SyncNS)/n, "sync-ns/append")
	}
	if n := float64(after.Count - before.Count); n > 0 {
		b.ReportMetric(float64(after.WaitNS-before.WaitNS)/n, "writer-wait-ns/write")
		b.ReportMetric(float64(after.HoldNS-before.HoldNS)/n, "writer-hold-ns/write")
	}
}
