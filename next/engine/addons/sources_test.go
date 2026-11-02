package addons

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/skills"
)

func TestNPMSourcesPinToOneVersion(t *testing.T) {
	var asked []string
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.EscapedPath())
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"latest":"1.2.3","beta":"2.0.0-rc.1"}`))
	}))
	defer registry.Close()
	s := &Sources{Registry: registry.URL, HTTP: registry.Client()}
	for source, want := range map[string][2]string{
		"npm:tools":               {"tools", "npm:tools@1.2.3"},
		"npm:tools@beta":          {"tools", "npm:tools@2.0.0-rc.1"},
		"npm:@scope/tools":        {"@scope/tools", "npm:@scope/tools@1.2.3"},
		"npm:@scope/tools@4.5.6":  {"@scope/tools", "npm:@scope/tools@4.5.6"},
		" npm:tools@1.0.0-beta.1": {"tools", "npm:tools@1.0.0-beta.1"},
	} {
		name, pinned, err := s.Resolve(context.Background(), source)
		if err != nil || name != want[0] || pinned != want[1] {
			t.Errorf("%s: %s %s %v", source, name, pinned, err)
		}
	}
	if !slices.Contains(asked, "/-/package/@scope%2Ftools/dist-tags") {
		t.Fatal(asked)
	}
	for source, want := range map[string]string{
		"npm:missing":       "no package",
		"npm:tools@^1":      "no tag",
		"npm:../etc":        "not an npm package name",
		"./local":           "not a Pi package source",
		"ext::sh -c id":     "not a Pi package source",
		"file:///etc/a/b":   "not a Pi package source",
		"https://host.test": "not a Pi package source",
	} {
		if _, _, err := s.Resolve(context.Background(), source); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", source, err)
		}
	}
}

func TestGitSourcesPinToOneCommit(t *testing.T) {
	const commit, tag, peeled = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"
	var calls [][]string
	s := &Sources{Git: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if strings.Contains(args[2], "gone") {
			return nil, errors.New("exit 128")
		}
		if args[len(args)-1] == "HEAD" {
			return []byte(commit + "\tHEAD\n"), nil
		}
		if args[3] == "nothing" {
			return nil, nil
		}
		return []byte(tag + "\trefs/tags/v1\n" + peeled + "\trefs/tags/v1^{}\n"), nil
	}}
	for source, want := range map[string][2]string{
		"https://github.com/o/r":             {"github.com/o/r", "https://github.com/o/r@" + commit},
		"https://github.com/o/r.git@v1":      {"github.com/o/r", "https://github.com/o/r.git@" + peeled},
		"git:github.com/o/r@v1":              {"github.com/o/r", "git:github.com/o/r@" + peeled},
		"git:https://example.test:8080/o/r":  {"example.test/o/r", "git:https://example.test:8080/o/r@" + commit},
		"git:git@github.com:o/r":             {"github.com/o/r", "git:git@github.com:o/r@" + commit},
		"ssh://git@example.test/o/r":         {"example.test/o/r", "ssh://git@example.test/o/r@" + commit},
		"https://github.com/o/r@" + tag + "": {"github.com/o/r", "https://github.com/o/r@" + tag},
	} {
		name, pinned, err := s.Resolve(context.Background(), source)
		if err != nil || name != want[0] || pinned != want[1] {
			t.Errorf("%s: %s %s %v", source, name, pinned, err)
		}
	}
	if got := calls[0][:2]; !slices.Equal(got, []string{"ls-remote", "--"}) {
		t.Fatal(calls[0])
	}
	if _, _, err := s.Resolve(context.Background(), "https://example.test/o/gone"); err == nil || !strings.Contains(err.Error(), "could not read") {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(context.Background(), "https://example.test/o/r@nothing"); err == nil || !strings.Contains(err.Error(), "no ref") {
		t.Fatal(err)
	}
}

func githubTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	w := tar.NewWriter(gz)
	for name, text := range files {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "repo-main/" + name, Mode: 0o644, Size: int64(len(text))}); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(text))
	}
	w.Close()
	gz.Close()
	return out.Bytes()
}

func TestASkillFromALink(t *testing.T) {
	const skillText = "---\nname: pdf-tools\ndescription: Read PDFs\n---\n"
	var fetched []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = append(fetched, r.URL.Path)
		switch r.URL.Path {
		case "/raw/SKILL.md":
			w.Write([]byte(skillText))
		case "/raw/page.html":
			w.Write([]byte("<html>"))
		case "/o/r/tar.gz/main", "/o/r/tar.gz/HEAD":
			w.Write(githubTarball(t, map[string]string{"skills/pdf/SKILL.md": skillText, "skills/pdf/notes.txt": "n", "skills/other/SKILL.md": "x", "SKILL.md": skillText}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := &Sources{GitHub: server.URL, HTTP: server.Client()}

	data, err := s.Skill(context.Background(), server.URL+"/raw/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if meta, err := skills.Inspect(data); err != nil || meta.Name != "pdf-tools" {
		t.Fatalf("%+v %v", meta, err)
	}
	for _, link := range []string{"https://github.com/o/r/tree/main/skills/pdf", "https://github.com/o/r/blob/main/skills/pdf/SKILL.md", "https://github.com/o/r"} {
		data, err := s.Skill(context.Background(), link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if meta, err := skills.Inspect(data); err != nil || meta.Name != "pdf-tools" {
			t.Fatalf("%s: %+v %v", link, meta, err)
		}
	}
	if _, err := s.Skill(context.Background(), server.URL+"/raw/page.html"); err == nil || !strings.Contains(err.Error(), "not a SKILL.md") {
		t.Fatal(err)
	}
	if _, err := s.Skill(context.Background(), "https://github.com/o/r/tree/main/no/such"); err == nil || !strings.Contains(err.Error(), "has no SKILL.md") {
		t.Fatal(err)
	}
}
