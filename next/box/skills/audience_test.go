package skills

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestBuiltInAudience(t *testing.T) {
	old := builtIn
	t.Cleanup(func() { builtIn = old })
	builtIn = fstest.MapFS{
		"admin/SKILL.md":  &fstest.MapFile{Data: []byte("---\nname: admin\ndescription: Administration\ndyna-audience: root\n---\nSecret procedure")},
		"admin/script.sh": &fstest.MapFile{Data: []byte("echo admin")},
		"common/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: common\ndescription: Common procedure\n---\nCommon")},
	}
	if !BuiltIn()[0].RootOnly {
		t.Fatal("audience not carried to add-on list")
	}
	for _, root := range []bool{false, true} {
		dir := t.TempDir()
		if err := Install(dir, root); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"SKILL.md", "script.sh"} {
			_, err := os.Stat(filepath.Join(dir, "admin", file))
			if root && err != nil || !root && !os.IsNotExist(err) {
				t.Fatalf("root=%v %s: %v", root, file, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "common", File)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Parse([]byte("---\nname: test\ndescription: Test\ndyna-audience: worker\n---")); err == nil {
		t.Fatal("invalid audience accepted")
	}
}
