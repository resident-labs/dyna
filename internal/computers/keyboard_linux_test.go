//go:build linux

package computers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"testing"
)

func TestKeyboardMapPreservesBindings(t *testing.T) {
	original, codes, err := keyboardMap(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for r := rune(32); r <= 126; r++ {
		if _, ok := codes[textKey(r)]; !ok {
			t.Fatalf("missing printable ASCII %q", r)
		}
	}
	if codes[textKey('a')] != 30 || codes[textKey('A')] != 30 || !baseKeyboardShift[textKey('A')] || baseKeyboardShift[textKey('a')] {
		t.Fatal("ASCII must retain standard US physical positions and Shift levels")
	}
	actions := parseActions(t, `[{"type":"type","text":"λ世界"},{"type":"keypress","keys":["F35"]}]`)
	updated, next, err := keyboardMap(actions, codes)
	if err != nil {
		t.Fatal(err)
	}
	if original == updated {
		t.Fatal("new symbols did not extend the map")
	}
	for name, code := range codes {
		if next[name] != code {
			t.Fatalf("%s moved from %d to %d", name, code, next[name])
		}
	}
	for _, r := range "λ世界" {
		if _, ok := codes[textKey(r)]; ok {
			t.Fatal("map construction changed its input")
		}
		if _, ok := next[textKey(r)]; !ok {
			t.Fatalf("missing new symbol %q", r)
		}
	}
	repeated, _, err := keyboardMap(actions, next)
	if err != nil || repeated != updated {
		t.Fatalf("identical symbols changed the map: %v", err)
	}
}

func TestKeyboardMapExtrasKeepStandardUSKeys(t *testing.T) {
	// Enough new symbols to walk past every low position.
	text := make([]rune, 60)
	for i := range text {
		text[i] = rune(0x4e00 + i)
	}
	encoded, _ := json.Marshal(string(text))
	keymap, codes, err := keyboardMap(parseActions(t, `[{"type":"type","text":`+string(encoded)+`}]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range text {
		if code := codes[textKey(r)]; reservedKeyboardCode(code) {
			t.Fatalf("%q was bound to standard US key position %d", r, code)
		}
	}
	if codes[textKey('q')] != 16 {
		t.Fatalf("q moved to %d", codes[textKey('q')])
	}
	for _, line := range strings.Split(keymap, "\n") {
		var code uint32
		if _, err := fmt.Sscanf(line, "<K%d>=", &code); err == nil && reservedKeyboardCode(code) {
			t.Fatalf("keymap redefines standard key %d: %s", code, line)
		}
	}
}

func TestKeypressHoldCoversIdleFrame(t *testing.T) {
	if frame := time.Second / idleRefreshHz; keypressHold <= frame {
		t.Fatalf("hold %v does not outlast one idle frame (%v)", keypressHold, frame)
	}
}

func TestKeyboardMapExhaustionIsAtomic(t *testing.T) {
	_, codes, err := keyboardMap(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// More new symbols than physical positions must fail before any upload.
	text := make([]rune, len(keyboardCodes)+1)
	for i := range text {
		text[i] = rune(0x4e00 + i)
	}
	encoded, _ := json.Marshal(string(text))
	_, _, err = keyboardMap(parseActions(t, `[{"type":"type","text":`+string(encoded)+`}]`), codes)
	if err == nil {
		t.Fatal("exhausted physical keycodes were accepted")
	}
	for _, r := range text {
		if _, ok := codes[textKey(r)]; ok {
			t.Fatal("failed extension changed the old map")
		}
	}
}

func keyboardTestPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 2)
	for i, fd := range pair {
		file := os.NewFile(uintptr(fd), "keyboard-test")
		conn, err := net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = conn.(*net.UnixConn)
		t.Cleanup(func() { conn.Close() })
	}
	return connections[0], connections[1]
}

func TestKeyboardFailureInvalidatesHeldState(t *testing.T) {
	conn, peer := keyboardTestPair(t)
	peer.Close()
	keyboard := &virtualKeyboard{w: &wayland{conn: conn}, object: 3}
	if err := keyboard.key("CTRL", true); err == nil {
		t.Fatal("disconnected key delivery reported success")
	}
	if !keyboard.failed {
		t.Fatal("uncertain modifier state can be reused")
	}
	if _, err := conn.Write([]byte{0}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("failed keyboard was not closed: %v", err)
	}
}

func TestKeyboardPreparationDoesNotHoldStatusLock(t *testing.T) {
	conn, peer := keyboardTestPair(t)
	keymap, codes, err := keyboardMap(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyboard := &virtualKeyboard{w: &wayland{conn: conn, next: 3}, object: 3, keymap: keymap, codes: codes}
	comp := &process{}
	supervisor := &supervisor{compositor: comp, keyboard: keyboard}
	done := make(chan error, 1)
	go func() { _, err := supervisor.inputKeyboard(context.Background(), comp, "unused", nil); done <- err }()
	// Receiving the uploaded fd proves preparation reached compositor I/O.
	peer.SetReadDeadline(time.Now().Add(time.Second))
	body, oob := make([]byte, 16384), make([]byte, unix.CmsgSpace(4))
	_, n, _, _, err := peer.ReadMsgUnix(body, oob)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := unix.ParseSocketControlMessage(oob[:n])
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			t.Fatal(err)
		}
		for _, fd := range fds {
			unix.Close(fd)
		}
	}
	locked := make(chan struct{})
	go func() { supervisor.mu.Lock(); supervisor.mu.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(200 * time.Millisecond):
		t.Error("keyboard I/O blocked status/control lock")
	}
	peer.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("failed preparation reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preparation did not stop")
	}
}

func TestShortcutWaitFailsOpen(t *testing.T) {
	a := parseActions(t, `[{"type":"keypress","keys":["CTRL","l"]},{"type":"keypress","keys":["l"]},{"type":"keypress","keys":["CTRL"]},{"type":"type","text":"x"}]`)
	if !isShortcut(a[0]) || isShortcut(a[1]) || isShortcut(a[2]) || isShortcut(a[3]) {
		t.Fatal("only a modifier with an ordinary key is a shortcut")
	}
	started := time.Now()
	if err := shortcutSettle(context.Background(), errors.New("no screencopy")); err != nil {
		t.Fatalf("a damage error failed the action: %v", err)
	}
	if time.Since(started) < shortcutFallback {
		t.Fatal("a damage error skipped the fallback pause")
	}
	if err := shortcutSettle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := shortcutSettle(ctx, errors.New("no screencopy")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
}
