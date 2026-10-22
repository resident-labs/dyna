package workflowtemplate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDefaultRegistryDirUsesXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "unused-home"))
	got := DefaultRegistryDir()
	want := filepath.Join(root, "dyna", "templates")
	if got != want {
		t.Fatalf("DefaultRegistryDir() = %q, want %q", got, want)
	}
}

func TestNormalizeNameRejectsTraversalAndUnsafe(t *testing.T) {
	for _, name := range []string{
		"", ".", "..", "../etc", "..hidden", "/abs", "foo/bar", `foo\bar`,
		".hidden", "-leading", "has space", "foo.js.js/x", strings.Repeat("a", MaxNameLength+1),
	} {
		if _, err := NormalizeName(name); err == nil {
			t.Errorf("NormalizeName(%q) succeeded, want error", name)
		}
	}
	got, err := NormalizeName("Echo.js")
	if err != nil || got != "Echo" {
		t.Fatalf("NormalizeName(Echo.js) = %q, %v", got, err)
	}
}

func TestListSortedUserRegistryOnly(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	examples := filepath.Join(dir, "examples")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(examples, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJS(t, registry, "zeta.js", `export const meta = { name: 'user-zeta', description: 'from user' }
return 1`)
	writeJS(t, registry, "alpha.js", `return 1`)
	writeJS(t, examples, "alpha.js", `export const meta = { description: 'example alpha' }
return 1`)
	writeJS(t, examples, "mid.js", `return 1`)
	writeJS(t, examples, "not-a-template.txt", `return 1`)
	if err := os.WriteFile(filepath.Join(registry, ".hidden.js"), []byte(`return 1`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, ManagedManifestName), []byte(`{"files":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := New(registry).List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range list {
		got = append(got, string(item.Source)+":"+item.Name+":"+item.Description)
	}
	want := []string{
		"user:alpha:",
		"user:zeta:from user",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("list = %#v, want %#v", got, want)
	}
}

func TestListEmptyIsNonNil(t *testing.T) {
	list, err := New(filepath.Join(t.TempDir(), "missing-reg")).List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("list = %#v %v, want empty non-nil", list, err)
	}
}

func TestResolveNameUsesUserRegistryOnly(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	examples := filepath.Join(dir, "examples")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(examples, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJS(t, registry, "echo.js", `return {from: "user"}`)
	writeJS(t, examples, "echo.js", `return {from: "examples"}`)
	writeJS(t, examples, "only-example.js", `return {from: "examples"}`)

	cat := New(registry)
	info, src, err := cat.ResolveName("echo")
	if err != nil || info.Source != SourceUser || !strings.Contains(src, `"user"`) {
		t.Fatalf("echo = %#v %q %v", info, src, err)
	}
	if _, _, err := cat.ResolveName("only-example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cwd examples leaked into resolve: %v", err)
	}
}

func TestExtractMetaFallsBackToFilenameIdentity(t *testing.T) {
	name, desc := ExtractMeta(`return {ok: true}`)
	if name != "" || desc != "" {
		t.Fatalf("empty meta = %q %q", name, desc)
	}
	name, desc = ExtractMeta(`
const name = 'not-meta'
export const meta = { name: 'display', description: 'hello "world"' }
const description = 'later'
return 1
`)
	if name != "display" || desc != `hello "world"` {
		t.Fatalf("meta = %q %q", name, desc)
	}
}

func TestSaveValidatesAndWritesAtomically(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "registry")
	cat := New(registry)
	src := []byte("export const meta = { name: 'echo' }\nreturn args\n")
	result, err := cat.Save("echo", src, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "echo" || result.Bytes != len(src) || result.Overwritten {
		t.Fatalf("save result = %#v", result)
	}
	got, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(got, src) {
		t.Fatalf("saved bytes = %q %v", got, err)
	}
	info, err := os.Stat(result.Path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("saved mode = %v %v, want 0644", info, err)
	}
	if _, err := cat.Save("echo", src, false); !errors.Is(err, ErrExists) {
		t.Fatalf("collision error = %v", err)
	}
	updated := []byte("export const meta = { name: 'echo' }\nreturn {ok: true}\n")
	over, err := cat.Save("echo", updated, true)
	if err != nil || !over.Overwritten {
		t.Fatalf("overwrite = %#v %v", over, err)
	}
	got, err = os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(got, updated) {
		t.Fatalf("overwritten bytes = %q %v", got, err)
	}
}

func TestValidateSourceRejectsRetiredProfileReviewAPIs(t *testing.T) {
	for _, src := range []string{
		`rateProfile({profile: "terra", stars: 5, text: "excellent"})`,
		`review({profile: "terra", stars: 5, text: "legacy"})`,
	} {
		if err := ValidateSource([]byte(src)); err == nil {
			t.Fatalf("retired profile review API still marks a source as a Dyna workflow: %s", src)
		}
	}
}

func TestSaveRejectsMalformedAndOversized(t *testing.T) {
	cat := New(filepath.Join(t.TempDir(), "registry"))
	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{name: "empty", src: nil},
		{name: "nul", src: []byte("return 1\x00")},
		{name: "invalid utf8", src: []byte("return \xff")},
		{name: "not js", src: []byte("just some notes about the task")},
		{name: "oversized", src: append(bytes.Repeat([]byte("return 1\n"), 1), bytes.Repeat([]byte("x"), MaxSourceBytes)...)},
	} {
		if _, err := cat.Save("ok", tc.src, false); err == nil {
			t.Errorf("%s: Save succeeded", tc.name)
		}
	}
}

func TestListAndResolveSkipSymlinks(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.js")
	writeJS(t, dir, "target.js", `return 1`)
	if err := os.Symlink(target, filepath.Join(registry, "shadow.js")); err != nil {
		t.Fatal(err)
	}
	cat := New(registry)
	list, err := cat.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("list = %#v %v, want empty", list, err)
	}
	if _, _, err := cat.ResolveName("shadow"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve symlink = %v, want not found", err)
	}
}

func TestConcurrentSavesCollideExclusively(t *testing.T) {
	cat := New(filepath.Join(t.TempDir(), "registry"))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := []byte("return " + strings.Repeat("1", i+1) + "\n")
			_, err := cat.Save("race", src, false)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	var ok, collide int
	for err := range errs {
		if err == nil {
			ok++
			continue
		}
		if errors.Is(err, ErrExists) {
			collide++
			continue
		}
		t.Fatalf("unexpected save error: %v", err)
	}
	if ok != 1 || collide != 1 {
		t.Fatalf("ok=%d collide=%d, want 1 exclusive winner", ok, collide)
	}
	got, err := os.ReadFile(filepath.Join(cat.RegistryDir, "race.js"))
	if err != nil || !bytes.HasPrefix(got, []byte("return ")) {
		t.Fatalf("winner file = %q %v", got, err)
	}
}

func TestCopyWritesEditableDestination(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	src := []byte("export const meta = { name: 'echo', description: 'copy me' }\nreturn args\n")
	writeJS(t, registry, "echo.js", string(src))
	cat := New(registry)

	if _, err := cat.Copy("echo", "", false); err == nil {
		t.Fatal("copy accepted an empty destination")
	}
	if _, err := cat.Copy("echo", filepath.Join(dir, "review.txt"), false); err == nil {
		t.Fatal("copy accepted a non-js destination")
	}

	dest := filepath.Join(dir, "scratch", "review.js")
	result, err := cat.Copy("echo", dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Overwritten || result.Name != "echo" || result.Destination != dest {
		t.Fatalf("copy = %#v", result)
	}
	got, err := os.ReadFile(result.Destination)
	if err != nil || !bytes.Equal(got, src) {
		t.Fatalf("copied = %q %v", got, err)
	}
	info, err := os.Stat(result.Destination)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("copied mode = %v %v, want 0644", info, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "scratch", ".catalog.lock")); !os.IsNotExist(err) {
		t.Fatalf("destination lockfile should not exist: %v", err)
	}
	again, err := cat.Copy("echo", dest, false)
	if err != nil || again.Overwritten {
		t.Fatalf("identical copy = %#v %v", again, err)
	}

	if err := os.WriteFile(dest, []byte("return 'changed'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Copy("echo", dest, false); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite refusal = %v", err)
	}
	over, err := cat.Copy("echo", dest, true)
	if err != nil || !over.Overwritten {
		t.Fatalf("overwrite = %#v %v", over, err)
	}
	got, _ = os.ReadFile(dest)
	if !bytes.Equal(got, src) {
		t.Fatalf("overwritten bytes = %q", got)
	}
}

func TestCopyRejectsSymlinkDestinationAndTraversalName(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJS(t, registry, "echo.js", `return 1`)
	cat := New(registry)

	dest := filepath.Join(dir, "out", "echo.js")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside.js"), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Copy("echo", dest, true); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink dest error = %v", err)
	}
	if _, err := cat.Copy("../echo", filepath.Join(dir, "ok.js"), false); err == nil {
		t.Fatal("copy accepted a traversal name")
	}
}

func writeJS(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}
