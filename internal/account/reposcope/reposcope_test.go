package reposcope

import (
	"slices"
	"testing"
)

func TestScopedAccountRunsOnlyInItsRepos(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const work, personal = "work", "personal"
	if err := s.Set(work, []string{"github.com/acmecorp/portal", "github.com/acmecorp/api"}); err != nil {
		t.Fatal(err)
	}
	rules, err := s.Rules()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		account, repo string
		want          bool
	}{
		{work, "github.com/acmecorp/portal", true},
		{work, "github.com/resident-labs/dyna", false},
		{work, "", false},
		{personal, "github.com/acmecorp/portal", false},
		{personal, "github.com/resident-labs/dyna", true},
		{personal, "", true},
	} {
		if got := rules.Allows(c.account, c.repo); got != c.want {
			t.Errorf("Allows(%q, %q) = %v, want %v", c.account, c.repo, got, c.want)
		}
	}
}

func TestSetReplacesAndClears(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", []string{"h/x", "h/y"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", []string{"h/z"}); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.Rules()
	if !slices.Equal(rules["a"], []string{"h/z"}) {
		t.Fatalf("rules = %v", rules)
	}
	if err := s.Set("a", nil); err != nil {
		t.Fatal(err)
	}
	rules, _ = s.Rules()
	if len(rules) != 0 || !rules.Allows("b", "h/z") {
		t.Fatalf("a cleared list must free the repo: %v", rules)
	}
	if s.Set("a", []string{"bad repo"}) == nil || s.Set("", nil) == nil {
		t.Fatal("Set accepted a bad repo or empty account")
	}
}

func TestIdentityAcceptsUrlsAndShortNames(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/AcmeCorp/Portal.git": "github.com/acmecorp/portal",
		"git@github.com:acmecorp/portal.git":     "github.com/acmecorp/portal",
		"github.com/acmecorp/portal/":            "github.com/acmecorp/portal",
	} {
		if got, err := Identity(in); err != nil || got != want {
			t.Errorf("Identity(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "portal", "acmecorp/portal", "github.com/portal"} {
		if got, err := Identity(bad); err == nil {
			t.Errorf("Identity(%q) = %q, want an error", bad, got)
		}
	}
}
