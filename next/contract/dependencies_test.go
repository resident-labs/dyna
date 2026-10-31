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
	// A package uses the longest entry that is its path or a parent of it, so a
	// nested package such as engine/store/sqlite falls back to engine/store.
	allowed := map[string][]string{
		"contract": {}, "room": {"contract"}, "harness": {"contract"},
		"box": {"contract", "harness"}, "engine": {"contract", "room", "box", "harness"},
		"migrate":          {"contract", "engine/store"},
		"engine/lifecycle": {"contract", "box", "engine/store"},
		"engine/store":     {"contract", "room", "box"},
		"engine/approve":   {"contract", "engine/store"},
		"engine/model":     {"contract", "engine/child"},
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		path := fields[0]
		if !strings.HasPrefix(path, module) {
			continue
		}
		rel := strings.TrimPrefix(path, module)
		area := ""
		for entry := range allowed {
			if (rel == entry || strings.HasPrefix(rel, entry+"/")) && len(entry) > len(area) {
				area = entry
			}
		}
		if area == "" {
			continue
		}
		deps := allowed[area]
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
