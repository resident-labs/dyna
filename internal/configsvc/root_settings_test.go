package configsvc

import (
	"dyna-agent/internal/settings"
	"reflect"
	"testing"
)

func TestRootProfilePickerUsesEnabledProfilesAndPersists(t *testing.T) {
	isolate(t)
	disabled := mock("disabled")
	disabled.Disabled = true
	seed(t, mock("chosen"), disabled)
	items, err := Items()
	if err != nil {
		t.Fatal(err)
	}
	var root Item
	for _, it := range items {
		if it.Key == settings.KeyRootProfile {
			root = it
		}
	}
	if root.Value != "opus" || root.Kind != "select" || root.LiveVsRestart != settings.LiveNextOpen || !reflect.DeepEqual(root.Options, []string{"", "chosen"}) {
		t.Fatalf("root picker = %+v", root)
	}
	got, err := Set(t.Context(), settings.KeyRootProfile, "chosen")
	if err != nil || got.Value != "chosen" || got.Source != settings.SourceFile {
		t.Fatalf("set root = %+v, %v", got, err)
	}
}
