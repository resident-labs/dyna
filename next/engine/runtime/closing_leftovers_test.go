package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type holdingHost struct {
	holds bool
	err   error
}

func (h holdingHost) HoldsBox(context.Context, contract.BoxID) (bool, error) { return h.holds, h.err }

func TestClosingLeftovers(t *testing.T) {
	record := box.Box{ID: "box_1", Host: "lt-24-005", State: contract.BoxClosing}
	var nothing box.NothingOnHost
	if err := closingLeftovers(context.Background(), holdingHost{}, record); !errors.As(err, &nothing) || nothing.Box != "box_1" || nothing.Host != "lt-24-005" {
		t.Fatalf("a box with nothing on its host: %v", err)
	}
	if err := closingLeftovers(context.Background(), holdingHost{holds: true}, record); err != nil {
		t.Fatalf("a box with a container or volume: %v", err)
	}
	// A host that cannot say is not a host that holds nothing: the close retries.
	lost := errors.New("host unreachable")
	if err := closingLeftovers(context.Background(), holdingHost{err: lost}, record); !errors.Is(err, lost) || errors.As(err, &nothing) {
		t.Fatalf("an unreachable host: %v", err)
	}
}

type closingRecordStore struct {
	boxStore
	record box.Box
}

func (s closingRecordStore) Box(context.Context, contract.BoxID) (box.Box, error) {
	return s.record, nil
}

// A box that was never placed has nothing anywhere to archive.
func TestStartOfAnUnplacedClosingBoxFindsNothing(t *testing.T) {
	b := &Boxes{Store: closingRecordStore{record: box.Box{ID: "box_1", State: contract.BoxClosing}}}
	var nothing box.NothingOnHost
	if err := b.Start(context.Background(), "box_1"); !errors.As(err, &nothing) {
		t.Fatalf("err = %v", err)
	}
}
