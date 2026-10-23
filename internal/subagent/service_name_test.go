package subagent

import (
	"context"
	"strings"
	"testing"
)

// A user session named like a service session would block that service's
// start in the room, so only the service's own role may take the name.
func TestServiceNamesAreReservedForTheirRole(t *testing.T) {
	e := newTestEnv(t)
	for _, name := range []string{LibrarianName, RoleSteward} {
		_, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Name: name, Dir: t.TempDir(), Task: "x"})
		if err == nil || !strings.Contains(err.Error(), "reserved for the") {
			t.Errorf("user session named %q: err = %v", name, err)
		}
	}
	if err := checkServiceName(LibrarianName, RoleLibrarian); err != nil {
		t.Fatalf("the librarian may take its own name: %v", err)
	}
	if err := checkServiceName(RoleLibrarian, ""); err != nil {
		t.Fatalf("the role word itself stays free for user sessions: %v", err)
	}
}
