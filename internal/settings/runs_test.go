package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunsRetentionSetting(t *testing.T) {
	v := Defaults()
	if v.RunsRetention != 14*24*time.Hour || v.Source(KeyRunsRetention) != SourceDefault {
		t.Fatal(v)
	}
	for _, tc := range []struct {
		input string
		want  time.Duration
	}{
		{"14d", 14 * 24 * time.Hour}, {"24h", 24 * time.Hour}, {"30m", 30 * time.Minute}, {"0", 0}, {"off", 0},
	} {
		if err := v.Set(KeyRunsRetention, tc.input); err != nil {
			t.Fatal(err)
		}
		if v.RunsRetention != tc.want {
			t.Fatalf("%s: got %s", tc.input, v.RunsRetention)
		}
		v.path = filepath.Join(t.TempDir(), "settings.json")
		if err := v.Save(); err != nil {
			t.Fatal(err)
		}
		got, err := Load(v.path)
		if err != nil {
			t.Fatal(err)
		}
		if got.RunsRetention != tc.want || got.Source(KeyRunsRetention) != SourceFile {
			t.Fatal(got)
		}
	}
	for _, input := range []string{"-1d", "-1h", "forever", "999999999d", "NaNd", "1.5d"} {
		if err := v.Set(KeyRunsRetention, input); err == nil {
			t.Fatalf("accepted %q", input)
		}
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(`{"runs":{"retention":"`+input+`"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("loaded %q", input)
		}
	}
}
