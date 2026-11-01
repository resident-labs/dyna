package git

import (
	"context"
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
	repos := &Repositories{Rooms: fakeAccess{allowed: true}, Hosts: fakeHosts{cache}, Mirror: &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: &fakeRunner{}}}
	boxRepos, err := repos.Get(context.Background(), "ses_1", "host", true)
	if err != nil || !cache.called || len(boxRepos) != 1 || boxRepos[0].Name != "owner/repo" || boxRepos[0].Branch != "v2/ses_1" || boxRepos[0].Base != "main" {
		t.Fatalf("repos %+v, cache %+v, error %v", boxRepos, cache, err)
	}
}
