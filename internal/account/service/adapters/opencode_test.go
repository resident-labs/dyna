package adapters

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fakeOpenCode(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestOpenCodeModelIntegrationFromProvider(t *testing.T) {
	exe := fakeOpenCode(t, `case "$4" in
 /api/model) echo '[{"id":"mini","providerID":"opencode-go","name":"Mini","enabled":true,"limit":{"context":200000,"input":160000,"output":32000},"variants":[{"id":"low"},{"id":"high"}]}]' ;;
 /api/provider) echo '[{"id":"opencode-go","integrationID":"opencode"}]' ;;
 *) case "$3" in
 /api/model) echo '[{"id":"mini","providerID":"opencode-go","name":"Mini","enabled":true,"limit":{"context":200000,"input":160000,"output":32000},"variants":[{"id":"low"},{"id":"high"}]}]' ;;
 /api/provider) echo '[{"id":"opencode-go","integrationID":"opencode"}]' ;;
 esac ;;
 esac
`)
	ms, err := modelsOpenCode(context.Background(), exe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Integration != "opencode" || ms[0].Input != 160000 || ms[0].Output != 32000 {
		t.Fatalf("routing or native limits lost: %+v", ms)
	}
	if want := []Effort{{ID: "low", Model: "opencode-go/mini#low"}, {ID: "high", Model: "opencode-go/mini#high"}}; !reflect.DeepEqual(ms[0].Efforts, want) {
		t.Fatalf("variants = %+v", ms[0].Efforts)
	}
}
func TestNativeErrorsDoNotReturnOutput(t *testing.T) {
	exe := fakeOpenCode(t, `echo SECRET; echo OTHERSECRET >&2; exit 1`)
	_, err := run(context.Background(), exe, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("unsafe error", err)
	}
}

func TestOpenCodeEmptyMetadataFallsBackNativeDiscovery(t *testing.T) {
	exe := fakeOpenCode(t, `if [ "$1" = api ];then echo '{"location":{"directory":"/fixture"},"data":[]}';else echo 'opencode/native-model';fi`)
	models, err := modelsOpenCode(context.Background(), exe, nil)
	if err != nil || len(models) != 1 || models[0].ID != "opencode/native-model" || models[0].Source != "opencode models" {
		t.Fatalf("empty catalog suppressed fallback: %+v %v", models, err)
	}
}
