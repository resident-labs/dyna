package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionCheckInInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.SessionCheckInInterval != 15*time.Minute {
		t.Fatal(v.SessionCheckInInterval)
	}
	for _, value := range []string{"2m", "0s"} {
		if err := v.Set(KeySessionCheckInInterval, value); err != nil {
			t.Fatal(err)
		}
		if err := v.Save(); err != nil {
			t.Fatal(err)
		}
		v, err = Load(path)
		want, _ := time.ParseDuration(value)
		if err != nil || v.SessionCheckInInterval != want {
			t.Fatal(v, err)
		}
	}
	for _, bad := range []string{"-1m", "oops", "999999999999h"} {
		if err := v.Set(KeySessionCheckInInterval, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestOldGoalKeeperSettingsStillLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	old := `{"goals":{"keeperInterval":"15m","keeperProfile":"old","keeperTimeout":"3m","checkInInterval":"30m"}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path)
	if err != nil || v.SessionCheckInInterval != 15*time.Minute {
		t.Fatal(v, err)
	}
	for _, key := range []string{"goals.keeperTimeout", "goals.keeperProfile", "goals.checkInInterval"} {
		if err := v.Set(key, "3m"); err == nil {
			t.Fatalf("removed setting %s can still be set", key)
		}
	}
}
