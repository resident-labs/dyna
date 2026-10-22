package questions

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestQuestionReadCannotImportOutsideEngine(t *testing.T) {
	root := questionRoot(t)
	legacyQuestions(t, root)
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	owned := database.OwnsSchema()
	database.DisownSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		} else {
			database.DisownSchema()
		}
	})
	_, err = (&Store{Root: root}).Read()
	if err == nil || !strings.Contains(err.Error(), "only the engine") {
		t.Fatalf("non-engine read = %v; want migration error", err)
	}
	var imported int
	if err := db.QueryRow("SELECT count(*) FROM meta WHERE key='questions.imported'").Scan(&imported); err != nil || imported != 0 {
		t.Fatalf("non-engine imported = %d, %v", imported, err)
	}
	if err := oldQuestionReader(root); err != nil {
		t.Fatalf("non-engine installed old-binary guard: %v", err)
	}
}
