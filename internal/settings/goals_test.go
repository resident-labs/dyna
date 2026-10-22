package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGoalsSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.GoalKeeperTimeout != 5*time.Minute || v.GoalKeeperProfile != "" {
		t.Fatal(v)
	}
	for key, value := range map[string]string{KeyGoalKeeperTimeout: "2m", KeyGoalKeeperProfile: "cheap"} {
		if err := v.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	v, err = Load(path)
	if err != nil || v.GoalKeeperTimeout != 2*time.Minute || v.GoalKeeperProfile != "cheap" {
		t.Fatal(v, err)
	}
	if err := v.Set(KeyGoalKeeperProfile, ""); err != nil {
		t.Fatal(err)
	}
}

// A settings file from an older keeper still loads; its removed
// check-in keys are ignored.
func TestOldGoalKeeperSettingsStillLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	old := `{"goals":{"keeperInterval":"15m","nudgesPerGoal":12,"nudgesPerRoom":200,"keeperTimeout":"3m","checkInInterval":"30m","checkInReplyWait":"15m"}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path)
	if err != nil || v.GoalKeeperTimeout != 3*time.Minute {
		t.Fatal(v, err)
	}
	if err := v.Set("goals.checkInInterval", "3m"); err == nil {
		t.Fatal("a removed setting can still be set")
	}
}

func TestGoalKeeperTimeoutRejectsUnboundedValues(t *testing.T) {
	v := Defaults()
	for _, bad := range []string{"0", "0s", "-1m", "oops", "999999999999h"} {
		if err := v.Set(KeyGoalKeeperTimeout, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
