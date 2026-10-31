package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"
)

type profileService struct {
	Service
	engine *engine.Service
}

func (p profileService) ProfileList(ctx context.Context, who contract.Principal, req contract.ProfileListRequest) (contract.ProfileListResponse, *contract.Error) {
	return p.engine.ProfileList(ctx, who, req)
}

func TestProfileListOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"z":{"provider":"openai","model":"z-model","allowedModels":["z-model"],"thinking":"high"},"a":{"provider":"anthropic","model":"a-model","allowedModels":["a-model"],"thinking":"low"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := engine.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(profileService{engine: engine.New(st, nil, profiles, time.Now)}, nil)
	denied := request(a.handler(boxCaller()), "/v1/profile.list", "{}")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("session status %d", denied.Code)
	}
	got := request(a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}), "/v1/profile.list", "{}")
	var result contract.ProfileListResponse
	if got.Code != http.StatusOK {
		t.Fatalf("owner status %d: %s", got.Code, got.Body)
	}
	if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := []contract.ProfileSummary{{Name: "a", Provider: contract.ModelProviderAnthropic, Model: "a-model", AllowedModels: []string{"a-model"}, Thinking: contract.ThinkingLow}, {Name: "z", Provider: contract.ModelProviderOpenAI, Model: "z-model", AllowedModels: []string{"z-model"}, Thinking: contract.ThinkingHigh}}
	if !reflect.DeepEqual(result.Profiles, want) {
		t.Fatalf("profiles: %+v", result.Profiles)
	}
}
