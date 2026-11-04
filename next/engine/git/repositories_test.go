package git

import (
	"context"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type fakeHosts struct{ cache *fakeCache }

func (h fakeHosts) CacheHost(context.Context, contract.HostName) (CacheHost, error) {
	return h.cache, nil
}

func TestRepositoriesPrepareOnlySessionCache(t *testing.T) {
	cache := &fakeCache{}
	access := fakeAccess{allowed: true, name: "exec-only-usage", claimed: &claim{}}
	repos := &Repositories{Rooms: access, Hosts: fakeHosts{cache}, Mirror: &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: &fakeRunner{branches: "refs/heads/main\nrefs/heads/exec-only-usage\n"}}}
	boxRepos, err := repos.Get(context.Background(), "ses_1", "host", true)
	if err != nil || !cache.called || len(boxRepos) != 1 || boxRepos[0].Name != "owner/repo" || boxRepos[0].Branch != "exec-only-usage" || boxRepos[0].Base != "main" {
		t.Fatalf("repos %+v, cache %+v, error %v", boxRepos, cache, err)
	}
	// The session name is what it asks for. What it may not have is the mirror's branches and the protected names.
	want := map[string]bool{"main": true, "master": true, "HEAD": true, "exec-only-usage": true}
	if access.claimed.wanted != "exec-only-usage" || !reflect.DeepEqual(access.claimed.unavailable, want) {
		t.Fatalf("claim %+v", access.claimed)
	}
}

func TestASessionNameGitRefusesIsMadeValid(t *testing.T) {
	for name, want := range map[string]string{"a..b": "a-b", "a...b": "a-b", "fix.": "fix.-", "x.lock": "x-lock", "ok.name_1-2": "ok.name_1-2", "a.lock.": "a.lock.-"} {
		access := fakeAccess{allowed: true, name: name, claimed: &claim{}}
		repos := &Repositories{Rooms: access, Hosts: fakeHosts{&fakeCache{}}, Mirror: &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: &fakeRunner{}}}
		if _, err := repos.Get(context.Background(), "ses_1", "host", true); err != nil || access.claimed.wanted != want {
			t.Errorf("%q: asked for %q, want %q: %v", name, access.claimed.wanted, want, err)
		}
	}
}

func TestACloneThatHasABranchRecordedIsNotClaimedAgain(t *testing.T) {
	access := routeAccess()
	access.claimed = &claim{}
	repos := &Repositories{Rooms: access, Hosts: fakeHosts{&fakeCache{}}, Mirror: &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: &fakeRunner{}}}
	boxRepos, err := repos.Get(context.Background(), "ses_1", "host", true)
	if err != nil || boxRepos[0].Branch != "work" || access.claimed.wanted != "" {
		t.Fatalf("%+v, claim %+v, %v", boxRepos, access.claimed, err)
	}
}

func TestCanPushBranchRules(t *testing.T) {
	ctx := context.Background()
	access := routeAccess()
	for branch, want := range map[string]bool{"work": true, "work-topic": true, "work-a.b_c": true, "work-taken": false, "other": false, "main": false, "work/x": false, "work-a..b": false, "work-x.": false} {
		if got, err := CanPushBranch(ctx, access, "owner/repo", "ses_1", branch); err != nil || got != want {
			t.Errorf("%s: %v, %v, want %v", branch, got, err, want)
		}
	}
	if got, _ := CanPushBranch(ctx, access, "owner/repo", "ses_9", "work"); got {
		t.Error("a session with no record pushed another session's branch")
	}
	// A live session's recorded branch is only its own, even when another session's name followed by a topic spells it.
	access.recorded["ses_2"] = []string{"work-taken"}
	if got, _ := CanPushBranch(ctx, access, "owner/repo", "ses_2", "work-taken"); !got {
		t.Error("a session's own recorded branch was refused")
	}
}
