package main

import (
	"net/url"
	"strings"
)

type hostAction int

const (
	hostDrop hostAction = iota
	hostRelated
	hostExternal
)

// hostURLAction routes window.open at the host boundary. Only the app origin
// stays in the desktop; ordinary web links open in the system browser.
func hostURLAction(origin, target string) hostAction {
	app, err := url.Parse(origin)
	if err != nil || app.Host == "" {
		return hostDrop
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return hostDrop
	}
	if u.Scheme == app.Scheme && strings.EqualFold(u.Host, app.Host) {
		return hostRelated
	}
	return hostExternal
}

// hostMediaAllowed answers a microphone request. The voice call needs audio
// only, so camera and screen capture are denied. WebKitGTK denies a request
// the host does not answer.
//
// page is the view's top-level URI, not the frame that asked: WebKitGTK 2.52
// gives a user-media request no origin. So this checks only that the window
// still shows the app. What keeps card frames (the card origin) off the
// microphone is the permissions policy: a cross-origin frame may not call
// getUserMedia unless its iframe carries allow="microphone", and
// web/src/lib/files/cardFrameMicrophone.test.ts keeps that attribute out.
func hostMediaAllowed(origin, page string, audio, video, display bool) bool {
	return audio && !video && !display && hostURLAction(origin, page) == hostRelated
}
