package adapters

import (
	"context"
	"testing"
)

func TestLocationScopedMetadataEnvelope(t *testing.T) {
	exe := fakeOpenCode(t, `case "$3" in
 /api/model) echo '{"location":{"directory":"/fixture"},"data":[{"id":"m","providerID":"opencode-go","limit":{"context":200000,"output":64000}}]}' ;;
 /api/provider) echo '{"location":{"directory":"/fixture"},"data":[{"id":"opencode-go","integrationID":"opencode"}]}' ;;
 esac`)
	ms, err := modelsOpenCode(context.Background(), exe, nil)
	if err != nil || len(ms) != 1 || ms[0].Integration != "opencode" || ms[0].Context != 200000 {
		t.Fatalf("%+v %v", ms, err)
	}
}
