package configsvc

import (
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func withDialect(p profile.Profile, harness, dialect string) profile.Profile {
	p.Harness = harness
	p.Output = &workeroutput.Declaration{Dialect: dialect}
	return p
}

func supportItem(t *testing.T, s HarnessSupport, key string) SupportItem {
	t.Helper()
	for _, it := range s.Items {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("no %q item in %+v", key, s)
	return SupportItem{}
}

func TestProfilesReportHarnessSupport(t *testing.T) {
	isolate(t)
	seed(t,
		withDialect(mock("opus"), profile.HarnessClaudeCode, workeroutput.DialectClaudeStreamJSON),
		withDialect(mock("pi"), profile.HarnessPi, workeroutput.DialectPiJSON),
		mock("plain"),
	)
	list, err := Profiles()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]HarnessSupport{}
	for _, p := range list.Profiles {
		by[p.Name] = p.Support
	}

	if s := by["opus"]; s.Partial || s.Dialect != workeroutput.DialectClaudeStreamJSON {
		t.Fatalf("claude support = %+v", s)
	}
	pi := by["pi"]
	if pi.Partial {
		t.Fatalf("pi should be full: %+v", pi)
	}
	if usage := supportItem(t, pi, "usage"); usage.State != SupportYes {
		t.Fatalf("pi usage = %+v", usage)
	}
	if supportItem(t, pi, "toolEvents").State != SupportYes {
		t.Fatalf("pi tool events = %+v", pi)
	}

	plain := by["plain"]
	if plain.Dialect != workeroutput.DialectText || !plain.Partial {
		t.Fatalf("a profile with no declaration reads as plain text: %+v", plain)
	}
	for _, key := range []string{"toolEvents", "safePoints", "sessionResume", "usage"} {
		if it := supportItem(t, plain, key); it.State != SupportNo || it.Reason == "" || it.Chip == "" {
			t.Fatalf("plain %s = %+v", key, it)
		}
	}
	if supportItem(t, plain, "transcript").State != SupportYes {
		t.Fatalf("plain text keeps a transcript: %+v", plain)
	}
}

// Every dialect the registry accepts must map to a support view, and the
// view must agree with the table the adapters are tested against.
func TestSupportMatchesCapabilitiesForEveryDialect(t *testing.T) {
	for _, d := range workeroutput.Dialects() {
		c := workeroutput.Capabilities(d)
		s := supportFor(withDialect(mock("x"), profile.HarnessMock, d))
		if got := supportItem(t, s, "usage").State; got != c.Usage {
			t.Errorf("%s usage = %q, table says %q", d, got, c.Usage)
		}
		full := c.Transcript && c.ToolEvents && c.SafePoints && c.SessionResume && c.Usage == "yes"
		if s.Partial == full {
			t.Errorf("%s partial = %v but table full = %v", d, s.Partial, full)
		}
	}
}

func TestSupportUsesHarnessDefaultWithoutDeclaration(t *testing.T) {
	isolate(t)
	for _, h := range profile.Harnesses {
		p := profile.Profile{Harness: h}
		got := supportFor(p)
		want := workeroutput.Capabilities(p.EffectiveDialect())
		if got.Dialect != p.EffectiveDialect() || supportItem(t, got, "toolEvents").State == SupportYes != want.ToolEvents || supportItem(t, got, "usage").State != want.Usage {
			t.Fatalf("%s default support = %#v, want %#v", h, got, want)
		}
	}
}
