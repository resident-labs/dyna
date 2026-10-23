package settings

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestTrustedBotsRoundTrip(t *testing.T) {
	v, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.PRTrustedBots, []string{"coderabbitai", "copilot-pull-request-reviewer"}) {
		t.Fatal(v.PRTrustedBots)
	}
	if err := v.Set(KeyPRTrustedBots, `[]`); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(v.Path())
	if err != nil || len(got.PRTrustedBots) != 0 {
		t.Fatalf("empty trust list: %+v, %v", got.PRTrustedBots, err)
	}
}
