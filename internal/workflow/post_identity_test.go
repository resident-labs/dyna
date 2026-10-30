package workflow

import (
	"context"
	"reflect"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestPostIdentityIsStableAcrossResumeAndCountsIdenticalCalls(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	run, err := runstore.Create("post identity", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	script := `post('@room', 'same'); post('@room', 'same'); post('@room', 'other');`
	var keys []string
	poster := func(_ context.Context, _, key string, _ []string, _ string) (PostResult, error) {
		keys = append(keys, key)
		return PostResult{ID: "msg"}, nil
	}
	opts := Options{ScriptSrc: script, Store: postTestStore(), Run: run, WorkDir: t.TempDir(), JobNamespace: "original-run", Poster: poster}
	if _, err := executeTest(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	first := append([]string(nil), keys...)
	keys = nil
	if _, err := executeTest(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, keys) || len(first) != 3 || first[0] == first[1] || first[0] == first[2] {
		t.Fatalf("first keys %v; replay keys %v", first, keys)
	}
}
