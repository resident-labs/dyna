package pi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptRangeReadsOnlyAcknowledgedBytes(t *testing.T) {
	dir := t.TempDir()
	first := `{"type":"message","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"m1","message":{"role":"assistant","content":[{"type":"text","text":"visible"}]}}
`
	extra := `{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"text","text":"uncommitted"}]}}
`
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte(first+extra), 0600); err != nil {
		t.Fatal(err)
	}
	reader := TranscriptReader{Dir: dir, Acked: map[string]int64{"file": int64(len(first))}}
	items, _, _, err := reader.Read("file", 0, nil, map[contract.DeliverySeq]contract.TurnID{1: "t"}, "", 0, 6)
	if err != nil || len(items) != 1 || items[0].Text != "visible" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	items, _, _, err = reader.Read("unknown", 0, nil, nil, "", 0, 6)
	if err != nil || len(items) != 0 {
		t.Fatalf("unknown items=%+v err=%v", items, err)
	}
}
