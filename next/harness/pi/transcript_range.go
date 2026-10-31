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
func (r TranscriptReader) Read(path string, start int64, end *int64, deliveries map[contract.DeliverySeq]contract.TurnID, item string, skip, limit int) ([]contract.TranscriptItem, int, bool, error) {
	file, section, err := r.open(path, start, end)
	if err != nil || file == nil {
		return nil, 0, false, err
	}
	defer file.Close()
	return ReadTranscript(section, deliveries, path, item, skip, limit)
}

// HasPost checks a bounded range for an exact post result.
func (r TranscriptReader) HasPost(path string, start int64, end *int64, id contract.EventID) (bool, error) {
	file, section, err := r.open(path, start, end)
	if err != nil || file == nil {
		return false, err
	}
	defer file.Close()
	return TranscriptHasPost(section, id)
}
