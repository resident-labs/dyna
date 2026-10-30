package configsvc

import (
	"reflect"
	"testing"

	"dyna-agent/internal/authority"
)

func TestEditProfileFieldKeepsEnvAndOtherFields(t *testing.T) {
	isolate(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root})
	p := mock("opus")
	p.Description = "keep me"
	p.Env = map[string]string{"MAX_THINKING_TOKENS": "8000"}
	p.Taste = 8
	svc := &Service{}
	if err := svc.UpsertProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.EditProfileField(ctx, p.Name, "cost", "3"); err != nil {
		t.Fatal(err)
	}
	reg, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reg.Get(p.Name)
	if !ok {
		t.Fatal("profile missing")
	}
	want := p
	want.Cost = 3
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("one-field edit changed other fields: got %#v, want %#v", *got, want)
	}
}
