package folder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictDecode(t *testing.T) {
	tests := []struct{ name, text, want string }{
		{"unknown", "mystery: true", "line 1"},
		{"duplicate", "categories: []\ncategories: []", "line 2: duplicate"},
		{"anchor", "categories: &list []", "line 1: aliases"},
		{"alias", "categories: &list []\nroles: *list", "aliases"},
		{"document", "categories: []\n---\ncategories: []", "one YAML document"},
		{"permission", "roles:\n- name: Moderator\n  permissions: [manage_universe]", "line 3: unknown permission"},
		{"overwrite", "roles:\n- name: Moderator\n  allow: {everyone: [view_channel]}", "line 3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var server Server
			err := Decode("server.yaml", []byte(test.text), &server)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "server.yaml") {
				t.Fatalf("got %v, want %s", err, test.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkipsUnmanagedDescendants(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "server.yaml", "categories: [Community, Foreign]\n")
	writeTestFile(t, root, "channels/Foreign/UNMANAGED.md", "Owned by hchat\nSecond line")
	writeTestFile(t, root, "channels/Foreign/category.yaml", "not: valid: yaml:")
	writeTestFile(t, root, "channels/Foreign/new/channel.yaml", "bad:")
	writeTestFile(t, root, "channels/Community/category.yaml", "sync: true\nchannels: [general]")
	writeTestFile(t, root, "channels/Community/general/channel.yaml", "type: text")
	writeTestFile(t, root, "channels/Community/general/welcome.md", "---\ntitle: Welcome\nmentions: none\n---\nHello\n## Links\nRead this")
	folder, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(folder.Entries) != 3 {
		t.Fatalf("entries: %#v", folder.Entries)
	}
	if folder.Entries[2].Unmanaged != "Owned by hchat" {
		t.Fatalf("marker: %#v", folder.Entries[2])
	}
	if folder.Entries[1].Messages["welcome.md"].Title != "Welcome" {
		t.Fatal("missing message")
	}
}

func TestLoadRefusesManagedSymlink(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "server.yaml", "categories: []")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "channels")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("got %v", err)
	}
}

func TestMessageFrontMatter(t *testing.T) {
	for _, content := range []string{"---\nunknown: x\n---\nhello", "---\nmentions: everyone\n---\nhello", "---\nembed: '[]'\n---\nhello", "---\ntitle: x"} {
		if _, err := parseMessage("welcome.md", []byte(content)); err == nil {
			t.Fatalf("accepted %s", content)
		}
	}
	message, err := parseMessage("welcome.md", []byte("---\nembed: '{\"description\":\"hello\"}'\n---\nbody"))
	if err != nil || len(message.Embed) == 0 {
		t.Fatalf("got %#v %v", message, err)
	}
}
