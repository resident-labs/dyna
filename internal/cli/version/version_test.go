package version

import (
	"bytes"
	"runtime/debug"
	"testing"
)

func TestVersionReportsInstalledCommitAndDirtyState(t *testing.T) {
	originalRead, originalRevision := readBuildInfo, buildRevision
	t.Cleanup(func() { readBuildInfo, buildRevision = originalRead, originalRevision })
	for _, tc := range []struct {
		name, stamp, revision, fallback, want string
		dirty, noInfo                         bool
	}{
		{name: "vcs", stamp: "dev", revision: "abcdef1234567890", want: "dev+abcdef123456"},
		{name: "dirty", stamp: "dev", revision: "abcdef1234567890", dirty: true, want: "dev+abcdef123456-dirty"},
		{name: "fallback", stamp: "dev", fallback: "123456abcdef7890", want: "dev+123456abcdef"},
		{name: "missing-info", stamp: "dev", fallback: "123456abcdef7890", noInfo: true, want: "dev+123456abcdef"},
		{name: "vcs-preferred", stamp: "dev", revision: "abcdef1234567890", fallback: "123456abcdef7890", want: "dev+abcdef123456"},
		{name: "release", stamp: "v1.2.3", revision: "abcdef1234567890", want: "v1.2.3"},
		{name: "unknown", stamp: "dev", want: "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buildRevision = tc.fallback
			readBuildInfo = func() (*debug.BuildInfo, bool) {
				if tc.noInfo {
					return nil, false
				}
				info := &debug.BuildInfo{}
				if tc.revision != "" {
					info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.revision", Value: tc.revision})
				}
				if tc.dirty {
					info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.modified", Value: "true"})
				}
				return info, true
			}
			cmd := NewCommand(tc.stamp)
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got, want := out.String(), "dyna "+tc.want+"\n"; got != want {
				t.Fatalf("version = %q, want %q", got, want)
			}
		})
	}
}
