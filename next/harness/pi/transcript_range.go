package pi

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptReader reads only acknowledged transcript bytes.
type TranscriptReader struct {
	Dir   string
	Acked map[string]int64
}

func (r TranscriptReader) open(path string, start int64, end *int64) (*os.File, *io.SectionReader, error) {
	ack, ok := r.Acked[path]
	if !ok {
		return nil, nil, nil
	}
	file, err := os.Open(filepath.Join(r.Dir, path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if end != nil {
		ack = min(ack, *end)
	}
	return file, io.NewSectionReader(file, start, max(0, ack-start)), nil
}

// Read decodes a bounded range from one acknowledged transcript file.
func (r TranscriptReader) Read(path string, start int64, end *int64, deliveries map[contract.DeliverySeq]contract.TurnID, item string, skip, limit int) ([]TranscriptItem, int, bool, error) {
	file, section, err := r.open(path, start, end)
	if err != nil || file == nil {
		return nil, 0, false, err
	}
	defer file.Close()
	return ReadTranscript(section, deliveries, path, item, skip, limit)
}

// FindCalls marks candidate IDs found in a bounded range.
func (r TranscriptReader) FindCalls(path string, start int64, end *int64, ids map[string]bool) error {
	file, section, err := r.open(path, start, end)
	if err != nil || file == nil {
		return err
	}
	defer file.Close()
	return TranscriptFindCalls(section, ids)
}

// ReadRecent retains a bounded recent window and the last assistant text.
func (r TranscriptReader) ReadRecent(path string, start int64, end *int64, deliveries map[contract.DeliverySeq]contract.TurnID, limit int) ([]TranscriptItem, int, bool, error) {
	file, section, err := r.open(path, start, end)
	if err != nil || file == nil {
		return nil, 0, false, err
	}
	defer file.Close()
	return readTranscriptMode(section, deliveries, path, "", 0, limit, 16<<20, true)
}
