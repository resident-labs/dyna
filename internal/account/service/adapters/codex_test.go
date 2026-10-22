package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func codexFixture(t *testing.T, cases string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-fixture")
	log := filepath.Join(dir, "requests")
	t.Setenv("DYNA_ADAPTER_TEST_LOG", log)
	script := `#!/bin/sh
[ "$1" = app-server ] || exit 90
n=0
account=0
while IFS= read -r line; do
 printf '%s\n' "$line" >> "$DYNA_ADAPTER_TEST_LOG"
 case "$line" in
 *'"method":"initialized"'*) continue ;;
 esac
 n=$((n+1))
 case "$line" in
 *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$n" ;;
 ` + cases + `
 *) exit 91 ;;
 esac
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, log
}

func TestCodexModelsPagesAndFilters(t *testing.T) {
	path, _ := codexFixture(t, `*'"method":"model/list"'*)
 if [ "$n" = 2 ]; then
 printf '{"id":%s,"result":{"data":[{"id":"ui-id","model":"model-a","displayName":"Model A"},{"model":"hidden","hidden":true}],"nextCursor":"page2"}}\n' "$n"
 else
 printf '{"id":%s,"result":{"data":[{"model":"model-a"},{"model":"model-b","description":"B","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}],"defaultReasoningEffort":"high"}]}}\n' "$n"
 fi ;;`)
	models, err := modelsCodex(context.Background(), path, nil)
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[0].Name != "Model A" || models[1].ID != "model-b" || models[0].Source == "" {
		t.Fatalf("unexpected models: %+v %v", models, err)
	}
	want := []Effort{{ID: "low", Args: []string{"-c", "model_reasoning_effort=low"}}, {ID: "high", Default: true, Args: []string{"-c", "model_reasoning_effort=high"}}}
	if models[1].Description != "B" || !reflect.DeepEqual(models[1].Efforts, want) || models[0].Efforts != nil {
		t.Fatalf("efforts not read: %+v", models)
	}
}
func TestCodexStartupDeadlineIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hang")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := modelsCodex(ctx, path, nil)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("startup cleanup failed: %v, %s", err, time.Since(start))
	}
}
func TestCodexRejectsMalformedModelResponse(t *testing.T) {
	path, _ := codexFixture(t, `*'"method":"model/list"'*) printf '{"id":%s,"result":{}}\n' "$n" ;;`)
	if _, err := modelsCodex(context.Background(), path, nil); !errors.Is(err, ErrProbe) {
		t.Fatalf("accepted malformed catalog: %v", err)
	}
}

func TestCodexRejectsRepeatedModelCursor(t *testing.T) {
	path, _ := codexFixture(t, `*'"method":"model/list"'*) printf '{"id":%s,"result":{"data":[],"nextCursor":"same"}}\n' "$n" ;;`)
	if _, err := modelsCodex(context.Background(), path, nil); !errors.Is(err, ErrProbe) {
		t.Fatalf("accepted cursor loop: %v", err)
	}
}
func TestCodexRejectsOversizedFrame(t *testing.T) {
	path, _ := codexFixture(t, `*'"method":"model/list"'*) head -c 1100000 /dev/zero | tr '\000' x; printf '\n' ;;`)
	if _, err := modelsCodex(context.Background(), path, nil); !errors.Is(err, ErrProbe) {
		t.Fatalf("accepted oversized frame: %v", err)
	}
}
