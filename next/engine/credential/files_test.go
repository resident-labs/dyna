package credential

import (
	"os"
	"testing"
)

func TestFilesPrivateAtomicAndClear(t *testing.T) {
	files := &Files{Dir: t.TempDir() + "/accounts"}
	for _, token := range []string{"first", "second"} {
		if err := files.Write("../room", token); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(files.path("../room"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credential mode: %v", info.Mode())
		}
		got, err := files.Read("../room")
		if err != nil || got != token {
			t.Fatal("credential did not round trip")
		}
	}
	if err := files.Write("../room", "\n"); err == nil {
		t.Fatal("accepted empty credential")
	}
	if err := files.Clear("../room"); err != nil {
		t.Fatal(err)
	}
	if err := files.Clear("../room"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read("../room"); err == nil {
		t.Fatal("read removed credential")
	}
}
