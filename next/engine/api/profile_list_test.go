package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func (p profileService) ProfileSet(ctx context.Context, who contract.Principal, req contract.ProfileSetRequest) (contract.ProfileSetResponse, *contract.Error) {
	return p.engine.ProfileSet(ctx, who, req)
}

func (p profileService) ProfileRemove(ctx context.Context, who contract.Principal, req contract.ProfileRemoveRequest) (contract.ProfileRemoveResponse, *contract.Error) {
	return p.engine.ProfileRemove(ctx, who, req)
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

func TestProfileWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"original":{"provider":"openai","model":"old","allowedModels":["old"],"thinking":"high"}}`), 0600); err != nil {
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
	owner := a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}})
	body := `{"profile":{"name":"new","provider":"anthropic","model":"new-model","allowedModels":["new-model"],"thinking":"low","description":"notes","taste":9,"diagnostics":8,"intelligence":7,"cost":6}}`
	if got := request(a.handler(boxCaller()), "/v1/profile.set", body); got.Code != http.StatusForbidden {
		t.Fatalf("session: %d", got.Code)
	}
	if got := request(owner, "/v1/profile.set", body); got.Code != http.StatusOK {
		t.Fatalf("set: %d %s", got.Code, got.Body)
	}
	if got := request(owner, "/v1/profile.set", `{"profile":{"name":"original","provider":"openai","model":"old","allowedModels":["old"],"thinking":"high"}}`); got.Code != http.StatusOK {
		t.Fatalf("update: %d %s", got.Code, got.Body)
	}
	duplicate := `{"create":true,"profile":{"name":"new"}}`
	if got := request(owner, "/v1/profile.set", duplicate); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), `"profile_exists"`) {
		t.Fatalf("duplicate: %d %s", got.Code, got.Body)
	}
	model, _, err := profiles.Model("new")
	if err != nil || model.ID != "new-model" {
		t.Fatalf("live model: %+v %v", model, err)
	}
	reloaded, err := engine.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	list := reloaded.List()
	if len(list) != 2 || list[0].Description != "notes" || list[0].Taste != 9 || list[0].Diagnostics != 8 || list[0].Intelligence != 7 || list[0].Cost != 6 {
		t.Fatalf("persisted: %+v", list)
	}
	if got := request(owner, "/v1/profile.set", `{"profile":{"name":"bad","taste":11}}`); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid: %d", got.Code)
	}
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,1,1)"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','new','closing','',0)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	listed := request(owner, "/v1/profile.list", "{}")
	var listedProfiles contract.ProfileListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &listedProfiles); err != nil {
		t.Fatal(err)
	}
	if len(listedProfiles.Profiles) != 2 || listedProfiles.Profiles[0].InUse != 1 || listedProfiles.Profiles[1].InUse != 0 {
		t.Fatalf("usage: %+v", listedProfiles.Profiles)
	}
	if got := request(owner, "/v1/profile.remove", `{"name":"new"}`); got.Code != http.StatusConflict {
		t.Fatalf("in-use: %d %s", got.Code, got.Body)
	}
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE sessions SET closed_at=1 WHERE id='s'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := request(owner, "/v1/profile.remove", `{"name":"new"}`); got.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", got.Code, got.Body)
	}
	if _, _, err := profiles.Model("new"); err == nil {
		t.Fatal("removed profile remains live")
	}
	if got := request(owner, "/v1/profile.remove", `{"name":"original"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("last profile: %d", got.Code)
	}
}
