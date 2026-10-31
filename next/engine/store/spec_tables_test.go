package store

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestSpecTablesMatchSchema checks that the table names in spec section 4.2
// match the CREATE TABLE names in schema.sql.
func TestSpecTablesMatchSchema(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../docs/spec.md")
	if err != nil {
		t.Fatal(err)
	}
	_, afterHeading, found := strings.Cut(string(spec), "### 4.2 Tables")
	if !found {
		t.Fatal("spec.md has no section 4.2 Tables")
	}
	section, _, found := strings.Cut(afterHeading, "\n## 5. ")
	if !found {
		t.Fatal("spec.md section 4.2 has no end before section 5")
	}

	specTableCell := regexp.MustCompile("^`(\\w+)`$")
	specTables := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 2 {
			continue
		}
		if m := specTableCell.FindStringSubmatch(strings.TrimSpace(cells[1])); m != nil {
			specTables[m[1]] = true
		}
	}
	if len(specTables) == 0 {
		t.Fatal("spec.md section 4.2 has no table rows")
	}

	createTable := regexp.MustCompile(`(?m)^CREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	schemaTables := map[string]bool{}
	for _, m := range createTable.FindAllStringSubmatch(string(schema), -1) {
		if !strings.HasPrefix(m[1], "sqlite_") {
			schemaTables[m[1]] = true
		}
	}

	for _, name := range slices.Sorted(maps.Keys(schemaTables)) {
		if !specTables[name] {
			t.Errorf("table %s is in schema.sql but not in spec section 4.2", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(specTables)) {
		if !schemaTables[name] {
			t.Errorf("table %s is in spec section 4.2 but not in schema.sql", name)
		}
	}
}
