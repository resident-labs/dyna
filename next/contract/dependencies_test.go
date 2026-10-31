package contract_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDependencyDirection(t *testing.T) {
	const module = "github.com/resident-labs/dyna/"
	cmd := exec.Command("go", "list", "-deps", "-f", `{{.ImportPath}} {{.Standard}} {{join .Imports " "}}`, "./...")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	standard := make(map[string]bool)
	for _, line := range lines {
		fields := strings.Fields(line)
		standard[fields[0]] = fields[1] == "true"
	}
	// The runtime embeds the web assets; web has no Go dependencies on the engine.
	allowed := map[string][]string{
		"contract": {}, "room": {"contract"}, "harness": {"contract"},
		"box": {"contract", "harness"}, "engine": {"contract", "room", "box", "harness"},
		"migrate": {"contract", "engine/store"},
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		path := fields[0]
		if !strings.HasPrefix(path, module) {
			continue
		}
		area := strings.Split(strings.TrimPrefix(path, module), "/")[0]
		deps, checked := allowed[area]
		if !checked {
			continue
		}
		for _, imp := range fields[2:] {
			if path != module+"contract" && (strings.HasPrefix(imp, module+area+"/") || imp == module+area) {
				continue
			}
			ok := path == module+"engine/runtime" && imp == module+"web"
			for _, dep := range deps {
				ok = ok || imp == module+dep || strings.HasPrefix(imp, module+dep+"/")
			}
			if strings.HasPrefix(imp, module) && !ok || path == module+"contract" && !standard[imp] {
				t.Errorf("%s must not import %s", path, imp)
			}
		}
	}
}
