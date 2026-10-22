package configsvc

import (
	"dyna-agent/internal/browserconfig"
	"dyna-agent/internal/settings"
	"strings"
	"testing"
)

func TestBrowserItemsGatingAndLabels(t *testing.T) {
	v := settings.Defaults()
	v.BrowserProfileMode = "copy-mine"
	for _, available := range []bool{false, true} {
		items := browserItems(v, available, []browserconfig.Profile{{Directory: "Profile 2", Name: "Work"}})
		mode, owner := items[3], items[4]
		if mode.Value != "copy-mine" {
			t.Fatal("stored mode lost")
		}
		found := false
		for _, option := range mode.Options {
			if option == "copy-mine" {
				found = true
			}
		}
		if found != available {
			t.Fatalf("options = %#v", mode.Options)
		}
		if !available && !strings.Contains(mode.Help, "unavailable") {
			t.Fatal(mode.Help)
		}
		if owner.Options[0] != "Profile 2" || owner.OptionLabels[0] != "Work · Profile 2" {
			t.Fatal(owner)
		}
		for _, item := range items {
			if item.Help == "" || item.LiveVsRestart != settings.LiveNextOperation {
				t.Fatal(item)
			}
		}
	}
}
