package main

import "testing"

func TestBrowserGuideCommandsAreRegistered(t *testing.T) {
	for _, route := range [][]string{{"browser"}, {"browser", "open"}, {"browser", "stop"}} {
		command, _, err := newRootCommand().Find(route)
		if err != nil || command == nil || command.Name() != route[len(route)-1] {
			t.Errorf("documented command %v is not registered: %v", route, err)
		}
	}
}
