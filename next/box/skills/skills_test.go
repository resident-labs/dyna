package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const skillFile = "---\nname: pdf-tools\ndescription: Read PDFs\n---\n\nBody.\n"

func TestParseRefusesWhatPiWouldNotLoad(t *testing.T) {
	meta, err := Parse([]byte("\xef\xbb\xbf---\r\nname: pdf-tools\r\ndescription: >\r\n  Read PDFs\r\n---\r\nBody\r\n"))
	if err != nil || meta.Name != "pdf-tools" || meta.Description != "Read PDFs" {
		t.Fatalf("%+v %v", meta, err)
	}
	for text, want := range map[string]string{
		"no frontmatter":                             "frontmatter",
		"---\nname: a\ndescription: b\n":             "frontmatter",
		"---\nname: a\n---\n":                        "description",
		"---\ndescription: b\n---\n":                 "skill name",
		"---\nname: Bad_Name\ndescription: b\n---\n": "skill name",
		"---\nname: a--b\ndescription: b\n---\n":     "skill name",
		"---\nname: [\n---\n":                        "frontmatter",
	} {
		if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestTheDefaultSkillIsBuiltIn(t *testing.T) {
	items := BuiltIn()
	if len(items) != 2 || items[0].Name != "dyna-extend" || items[0].Description != "Use when you need to add a skill or mcp server to dyna" || !items[0].BuiltIn {
		t.Fatalf("%+v", items)
	}
	if !IsBuiltIn("dyna-extend") || IsBuiltIn("other") {
		t.Fatal("IsBuiltIn")
	}
	dir := t.TempDir()
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dyna-extend", File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dyna skill save", "dyna package add", "dyna mcp add", "next box start", "unless the owner tells you otherwise", ".pi/skills"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
}

func TestAnArchiveCarriesAFolderAndStaysTheSame(t *testing.T) {
	dir := t.TempDir()
	for path, mode := range map[string]os.FileMode{File: 0o644, "scripts/run.sh": 0o755, ".git/config": 0o644} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(skillFile), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	req, err := Request(dir, "", "room", false)
	if err != nil || req.Room != "room" {
		t.Fatalf("%+v %v", req.Room, err)
	}
	again, err := Request(dir, "", "room", false)
	if err != nil || string(again.Archive) != string(req.Archive) {
		t.Fatal("the same folder packed differently")
	}
	meta, err := Inspect(req.Archive)
	if err != nil || meta.Name != "pdf-tools" {
		t.Fatalf("%+v %v", meta, err)
	}
	out := t.TempDir()
	if err := Extract(req.Archive, out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, "scripts", "run.sh"))
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("%v %v", info, err)
	}
	for _, gone := range []string{".git", "link"} {
		if _, err := os.Lstat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s came along", gone)
		}
	}
}

func TestExtractStaysInItsFolder(t *testing.T) {
	archive := NewArchive()
	if err := archive.Add("../outside", 0o600, []byte("x")); err != nil {
		t.Fatal(err)
	}
	data, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := Extract(data, t.TempDir()); err == nil {
		t.Fatal("a name left the folder")
	}
}

func TestRequestFromALinkOrAFile(t *testing.T) {
	req, err := Request("https://example.test/SKILL.md", "", "", true)
	if err != nil || req.URL == "" || req.Archive != nil || !req.Global {
		t.Fatalf("%+v %v", req, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine.md"), []byte(skillFile), 0o600); err != nil {
		t.Fatal(err)
	}
	req, err = Request("mine.md", dir, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if meta, err := Inspect(req.Archive); err != nil || meta.Name != "pdf-tools" {
		t.Fatalf("%+v %v", meta, err)
	}
	if _, err := Request(t.TempDir(), "", "", false); err == nil || !strings.Contains(err.Error(), "has no SKILL.md") {
		t.Fatal(err)
	}
}
