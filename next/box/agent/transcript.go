package agent

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/resident-labs/dyna/contract"
)

type TranscriptAppender interface {
	Append(context.Context, contract.TranscriptAppendRequest) (int64, error)
}

// SendTranscript sends each chunk from the acknowledged byte offset.
// The caller invokes it again after the file changes or the socket reconnects.
func SendTranscript(ctx context.Context, client TranscriptAppender, path string, offset int64) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	buf := make([]byte, 32<<10)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			acked, err := client.Append(ctx, contract.TranscriptAppendRequest{Path: path, Offset: offset, Bytes: buf[:n]})
			if err != nil {
				return offset, err
			}
			if acked < offset || acked > offset+int64(n) {
				return offset, fmt.Errorf("invalid transcript acknowledgement %d", acked)
			}
			if acked == offset {
				return offset, fmt.Errorf("transcript acknowledgement made no progress")
			}
			offset = acked
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				return offset, err
			}
		}
		if readErr == io.EOF {
			return offset, nil
		}
		if readErr != nil {
			return offset, readErr
		}
	}
}
