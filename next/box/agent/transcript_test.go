package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type appendFake struct {
	calls []contract.TranscriptAppendRequest
}

func (f *appendFake) Append(_ context.Context, req contract.TranscriptAppendRequest) (int64, error) {
	req.Bytes = append([]byte(nil), req.Bytes...)
	f.calls = append(f.calls, req)
	if len(f.calls) == 1 {
		return req.Offset + 2, nil
	}
	return req.Offset + int64(len(req.Bytes)), nil
}

func TestTranscriptResendsFromAcknowledgedOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &appendFake{}
	offset, err := SendTranscript(context.Background(), fake, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 6 || len(fake.calls) != 2 || fake.calls[0].Offset != 1 || string(fake.calls[0].Bytes) != "bcdef" || fake.calls[1].Offset != 3 || string(fake.calls[1].Bytes) != "def" {
		t.Fatalf("offset=%d calls=%+v", offset, fake.calls)
	}
}
