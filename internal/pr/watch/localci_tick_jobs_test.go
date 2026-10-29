package watch

import (
	"slices"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

func TestTickJobs(t *testing.T) {
	tests := []struct {
		name             string
		wanted, headJobs []registeredCIJob
		modes            map[string]string
		paused           map[string]bool
		haveNodes        bool
		want             []string
	}{
		{
			name: "trials precede pull request heads in created time order",
			wanted: []registeredCIJob{
				{record: Record{Kind: "local", Repo: "owner/repo", RepoPath: "/checkout"}, head: "older", trial: true, created: time.Unix(1, 0)},
				{record: Record{Kind: "local", Repo: "owner/repo", RepoPath: "/checkout"}, head: "newer", trial: true, created: time.Unix(2, 0)},
			},
			headJobs: []registeredCIJob{
				{record: Record{Kind: "github", Repo: "owner/repo", RoomID: "room", RepoPath: "/checkout"}, head: "head"},
			},
			modes:     map[string]string{"room": localci.ModeLocal},
			haveNodes: true,
			want:      []string{"older", "newer", "head"},
		},
		{
			name: "a key in both lists appears once",
			wanted: []registeredCIJob{
				{record: Record{Kind: "github", Repo: "owner/repo", RoomID: "room", RepoPath: "/checkout"}, head: "shared", trial: true},
			},
			headJobs: []registeredCIJob{
				{record: Record{Kind: "github", Repo: "owner/repo", RoomID: "room", RepoPath: "/checkout"}, head: "shared"},
			},
			modes:     map[string]string{"room": localci.ModeLocal},
			haveNodes: true,
			want:      []string{"shared"},
		},
		{
			name: "a hosted job in a paused room is skipped",
			headJobs: []registeredCIJob{
				{record: Record{Kind: "github", Repo: "owner/repo", RoomID: "room", RepoPath: "/checkout"}, head: "paused"},
			},
			modes:     map[string]string{"room": localci.ModeLocal},
			paused:    map[string]bool{"room": true},
			haveNodes: true,
		},
		{
			name: "GitHub mode skips hosted work but keeps a local trial",
			wanted: []registeredCIJob{
				{record: Record{Kind: "local", Repo: "owner/repo", RoomID: "room", RepoPath: "/checkout"}, head: "local", trial: true},
				{record: Record{Kind: "github", Repo: "owner/repo", RoomID: "room", RepoPath: "/checkout"}, head: "hosted", trial: true},
			},
			modes:     map[string]string{"room": localci.ModeGitHub},
			haveNodes: true,
			want:      []string{"local"},
		},
		{
			name: "no enabled node gives no jobs",
			wanted: []registeredCIJob{
				{record: Record{Kind: "local", Repo: "owner/repo", RepoPath: "/checkout"}, head: "waiting", trial: true},
			},
		},
		{
			name: "a job without a checkout is dropped",
			wanted: []registeredCIJob{
				{record: Record{Kind: "local", Repo: "owner/repo"}, head: "missing", trial: true},
			},
			haveNodes: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jobs, desired := tickJobs(test.wanted, test.headJobs, test.modes, test.paused, test.haveNodes)
			got := make([]string, len(jobs))
			for i, job := range jobs {
				got[i] = job.head
				if _, ok := desired[registeredJobKey(job)]; !ok {
					t.Errorf("desired does not contain admitted job %q", job.head)
				}
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("jobs = %v, want %v", got, test.want)
			}
			if len(desired) != len(test.want) {
				t.Errorf("desired has %d keys, want %d", len(desired), len(test.want))
			}
		})
	}
}
