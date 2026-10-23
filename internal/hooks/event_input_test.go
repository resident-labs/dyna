package hooks

import "testing"

func TestValidateRejectsRetiredMainMoved(t *testing.T) {
	h := Hook{Name: "example", Event: "main-moved", Command: "true", Cwd: "main", Timeout: "1s", OnFail: "alert"}
	if err := Validate(h); err == nil {
		t.Fatal("retired event accepted")
	}
	h.Event = MainUpdated
	if err := Validate(h); err != nil {
		t.Fatal(err)
	}
}
