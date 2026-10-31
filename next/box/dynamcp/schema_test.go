package dynamcp

import "testing"

func TestRegistryBuilds(t *testing.T) {
	list, actions, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || len(actions) == 0 {
		t.Fatal("empty registry")
	}
}
