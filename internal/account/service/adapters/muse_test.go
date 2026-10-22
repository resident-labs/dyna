package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func museServeFixture(t *testing.T, initialize, cases string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "muse-fixture")
	log := filepath.Join(dir, "requests")
	t.Setenv("DYNA_ADAPTER_TEST_LOG", log)
	script := `#!/bin/sh
[ "$*" = 'serve --disable-shell --disable-write --no-session-log' ] || exit 90
[ "$MUSE_NO_AUTO_UPDATE" = 1 ] || exit 91
n=0
account=0
ready=0
while IFS= read -r line; do
 printf '%s\n' "$line" >> "$DYNA_ADAPTER_TEST_LOG"
 case "$line" in
 *'"jsonrpc":"2.0"'*) ;;
 *) exit 92 ;;
 esac
 case "$line" in
 *'"method":"initialized"'*) ready=1; continue ;;
 esac
 n=$((n+1))
 case "$line" in
 *'"method":"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":` + initialize + `}\n' "$n" ;;
 *) [ "$ready" = 1 ] || exit 93
 case "$line" in
 ` + cases + `
 *) exit 94 ;;
 esac ;;
 esac
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, log
}

const museInitializeFixture = `{"schema":{"version":1,"fingerprint":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"experimentalApi":true,"grantedCapabilities":[]}`

func TestMuseModelsUseNativeCatalogWithoutClaimingAuth(t *testing.T) {
	// A stable model query works even if the host does not grant experimental API.
	initialize := `{"schema":{"version":1},"experimentalApi":false,"grantedCapabilities":[]}`
	path, log := museServeFixture(t, initialize, `*'"method":"model/list"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"providerId":"meta","source":"fixture","profileId":null,"models":[{"modelId":"model-a","providerId":"meta","displayLabel":"Model A","contextLimit":200000,"outputLimit":32000},{"modelId":"model-a","providerId":"meta"},{"modelId":"model-b","providerId":"meta","contextLimit":null,"outputLimit":null,"variants":["unknown","low","high"],"defaultReasoningEffort":"high","reasoningEffortVariants":[{"tier":"high"}]}]}}\n' "$n" ;;`)
	models, err := modelsMuse(context.Background(), path, nil)
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[0].Context != 200000 || models[0].Output != 32000 || models[1].ID != "model-b" || !strings.Contains(models[0].Source, "fixture") {
		t.Fatalf("bad catalog: %+v %v", models, err)
	}
	want := []Effort{{ID: "low", Args: []string{"--reasoning-effort", "low"}}, {ID: "high", Default: true, Args: []string{"--reasoning-effort", "high"}}}
	if models[0].Efforts != nil || !reflect.DeepEqual(models[1].Efforts, want) {
		t.Fatalf("efforts: %+v", models)
	}
	raw, _ := os.ReadFile(log)
	if strings.Contains(string(raw), `"method":"account/read"`) || strings.Contains(string(raw), `"sessionId"`) {
		t.Fatal("catalog requested an account or session")
	}
}
func TestMuseRejectsMalformedProtocolAndCatalog(t *testing.T) {
	for _, result := range []string{`{"jsonrpc":"1.0","id":2,"result":{"models":[],"providerId":"meta"}}`, `{"jsonrpc":"2.0","id":2,"result":{}}`, `{"jsonrpc":"2.0","id":2,"result":{"models":[{"modelId":"x","providerId":"other"}],"providerId":"meta"}}`} {
		path, _ := museServeFixture(t, museInitializeFixture, `*'"method":"model/list"'*) printf '%s\n' '`+result+`' ;;`)
		if _, err := modelsMuse(context.Background(), path, nil); !errors.Is(err, ErrProbe) {
			t.Fatalf("accepted malformed catalog: %v", err)
		}
	}
}
