package settings

import (
	"fmt"
	"io"
	"os"
)

const DefaultContentBytes = 25 << 20

// MaxContentBytes keeps all eight-file sums, base64 expansion and JSON
// overhead representable on this platform and exactly representable in JS.
const MaxContentBytes = min(int64(^uint(0)>>1)/16, int64(1<<53-1)/16)

type ContentLimits struct {
	CardBytes int64
	FileBytes int64
}

func parseContentBytes(key, value string) (int64, error) {
	n, err := ParseBytes(value)
	if err != nil || n < 1 || n > MaxContentBytes {
		return 0, fmt.Errorf("%s: size must be positive whole bytes below %s; use 25 MiB, 25MB, 25m or 26214400 (MB means MiB)", key, FormatBytes(MaxContentBytes+1))
	}
	return n, nil
}

// Content reads settings for each new operation; no host restart is needed.
func Content() (ContentLimits, error) {
	v, err := Load(DefaultPath())
	if err != nil {
		return ContentLimits{}, err
	}
	return ContentLimits{v.MaxCardBytes, v.MaxFileBytes}, nil
}

// PostBytes accommodates eight files, or one card and seven files.
func (c ContentLimits) PostBytes() int64 { return 7*c.FileBytes + max(c.FileBytes, c.CardBytes) }

// JSONBytes covers base64 padding for each file plus 1 MiB of JSON metadata.
func (c ContentLimits) JSONBytes() int64 { return ((c.PostBytes()+2)/3)*4 + 8*4 + (1 << 20) }

// ReadContentFile bounds both the initial stat and a file that grows during read.
func ReadContentFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is too large (maximum %s)", path, FormatBytes(limit))
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is too large (maximum %s)", path, FormatBytes(limit))
	}
	return b, nil
}

// ReadStoredFile keeps already stored content readable after limits shrink.
// The current size bounds the read, including a concurrent file growth.
func ReadStoredFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() >= int64(^uint(0)>>1) {
		return nil, fmt.Errorf("stored file is too large to read")
	}
	return ReadContentFile(path, st.Size())
}
