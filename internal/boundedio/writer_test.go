package boundedio

import (
	"io"
	"os/exec"
	"strings"
	"testing"
)

// TestWriterLimitsSubprocessOutput is the subprocess drain test. It moved here
// from internal/account/native. The child writes 40000 bytes into a 100 byte
// writer: the writer keeps 100 bytes and still drains the stream to EOF, so
// the child exits instead of blocking on a full pipe.
func TestWriterLimitsSubprocessOutput(t *testing.T) {
	out := New(100)
	cmd := exec.Command("sh", "-c", "head -c 40000 /dev/zero")
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 100 || !out.Truncated() {
		t.Fatalf("kept %d bytes, truncated %v; want 100 bytes and truncated", out.Len(), out.Truncated())
	}
}

func TestWriterLimitKeepsFirstBytes(t *testing.T) {
	out := New(4)
	if n, err := out.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("first write: %d %v", n, err)
	}
	if out.Truncated() {
		t.Fatal("truncated before the limit")
	}
	if n, err := out.Write([]byte("defg")); err != nil || n != 4 {
		t.Fatalf("second write: %d %v", n, err)
	}
	if !out.Truncated() || string(out.Bytes()) != "abcd" {
		t.Fatalf("kept %q, truncated %v; want \"abcd\" and truncated", out.Bytes(), out.Truncated())
	}
	if n, err := out.Write(nil); err != nil || n != 0 {
		t.Fatalf("empty write: %d %v", n, err)
	}
}

func TestWriterTailKeepsLastBytes(t *testing.T) {
	out := NewTail(8)
	if n, err := out.Write([]byte("12345")); err != nil || n != 5 {
		t.Fatalf("first write: %d %v", n, err)
	}
	if out.Truncated() {
		t.Fatal("truncated before the limit")
	}
	if n, err := out.Write([]byte("6789ab")); err != nil || n != 6 {
		t.Fatalf("second write: %d %v", n, err)
	}
	if !out.Truncated() || string(out.Bytes()) != "456789ab" {
		t.Fatalf("kept %q, truncated %v; want \"456789ab\" and truncated", out.Bytes(), out.Truncated())
	}
}

// TestWriterReadFromDrainsToEOF proves io.Copy moves the whole stream through
// Write and reports the full count even though nothing past the limit stays.
func TestWriterReadFromDrainsToEOF(t *testing.T) {
	for _, out := range []*Writer{New(10), NewTail(10)} {
		input := strings.Repeat("x", 1<<20)
		copied, err := io.Copy(out, strings.NewReader(input))
		if err != nil || copied != int64(len(input)) {
			t.Fatalf("copied %d bytes, %v; want %d", copied, err, len(input))
		}
		if out.Len() != 10 || !out.Truncated() {
			t.Fatalf("kept %d bytes, truncated %v; want 10 bytes and truncated", out.Len(), out.Truncated())
		}
	}
}
