package skills

import (
	"strings"
	"testing"
)

func TestDiscordFolderReference(t *testing.T) {
	data, err := builtIn.ReadFile("dyna-discord/" + File)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") > 150 || !strings.Contains(string(data), "folder.md") {
		t.Fatal("Discord skill must link folder reference within 150 lines")
	}
	reference, err := builtIn.ReadFile("dyna-discord/folder.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"rooms/ROOM/discord/", "Never put this folder in a repository", "UNMANAGED.md", "server.yaml", "category.yaml", "channel.yaml", "policy.yaml", "hooks.yaml", "Apply is not available yet", "no message history"} {
		if !strings.Contains(string(reference), phrase) {
			t.Errorf("folder reference missing %q", phrase)
		}
	}
}
