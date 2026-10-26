package visual

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// A long TMPDIR, such as a session scratch directory, made Chromium abort at
// start, because its singleton socket path did not fit in the kernel limit.
func TestBrowserTempDirFitsChromiumSocket(t *testing.T) {
	long := "/tmp/" + strings.Repeat("d", 120)
	dir, err := browserTempDir([]string{long, "/tmp"})
	if err != nil {
		t.Fatalf("no usable temporary directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if strings.HasPrefix(dir, long) {
		t.Errorf("kept the long temporary directory %q", dir)
	}
	if len(dir)+len(chromiumSocketTail) >= chromiumSocketLimit {
		t.Errorf("Chromium socket path in %q is too long", dir)
	}
	if env := browserEnv(dir); !slices.Contains(env, "TMPDIR="+dir) {
		t.Errorf("Chromium environment does not set TMPDIR to %q", dir)
	}
	if _, err := browserTempDir([]string{long}); err == nil {
		t.Error("a long temporary directory produced no error")
	}
}
