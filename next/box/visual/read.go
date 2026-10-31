package visual

import (
	"io"
	"os"
)

// ReadFile bounds the bytes read from an HTML visual.
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, (25<<20)+1))
}
