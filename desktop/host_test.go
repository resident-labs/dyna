package main

import "testing"

func TestHostURLAction(t *testing.T) {
	const origin = "http://127.0.0.1:7457"
	for _, tc := range []struct {
		name, target string
		want         hostAction
	}{
		{"computer popout", origin + "/#pop=room_20261018-132045-12345678:ses_test", hostRelated},
		{"same origin page", origin + "/settings", hostRelated},
		{"external link", "https://github.com/resident-labs/dyna", hostExternal},
		{"other local port", "http://127.0.0.1:7458/rich/card", hostExternal},
		{"other scheme", "https://127.0.0.1:7457/", hostExternal},
		{"file", "file:///etc/passwd", hostDrop},
		{"script", "javascript:alert(1)", hostDrop},
		{"data", "data:text/html,hello", hostDrop},
		{"relative", "/#pop=test", hostDrop},
		{"empty", "", hostDrop},
		{"invalid", "http://[", hostDrop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostURLAction(origin, tc.target); got != tc.want {
				t.Fatalf("hostURLAction(%q) = %d, want %d", tc.target, got, tc.want)
			}
		})
	}
	if got := hostURLAction("://", origin); got != hostDrop {
		t.Fatalf("invalid app origin gave action %d", got)
	}
}

func TestHostMediaAllowed(t *testing.T) {
	const origin = "http://127.0.0.1:7457"
	// page is the window's top-level URI. A frame's own origin is not
	// known here; the iframe permissions policy guards card frames.
	for _, tc := range []struct {
		name, page            string
		audio, video, display bool
		want                  bool
	}{
		{"app microphone", origin + "/", true, false, false, true},
		{"app camera", origin + "/", false, true, false, false},
		{"app camera and microphone", origin + "/", true, true, false, false},
		{"app screen with audio", origin + "/", true, false, true, false},
		{"neither", origin + "/", false, false, false, false},
		{"window navigated away", "https://example.com/", true, false, false, false},
		{"no page", "", true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostMediaAllowed(origin, tc.page, tc.audio, tc.video, tc.display); got != tc.want {
				t.Fatalf("hostMediaAllowed(%q, audio=%v, video=%v, display=%v) = %v, want %v", tc.page, tc.audio, tc.video, tc.display, got, tc.want)
			}
		})
	}
}
