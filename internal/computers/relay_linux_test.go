//go:build linux

package computers

import (
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func unixPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := []*net.UnixConn{}
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c.(*net.UnixConn))
		t.Cleanup(func() { c.Close() })
	}
	return conns[0], conns[1]
}
func TestRelayPassesDescriptorsBothDirections(t *testing.T) {
	client, a := unixPair(t)
	b, server := unixPair(t)
	done := make(chan error, 1)
	go func() { done <- Relay(a, b) }()
	for _, pair := range [][2]*net.UnixConn{{client, server}, {server, client}} {
		file, err := os.CreateTemp(t.TempDir(), "shared")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.WriteString("shared buffer"); err != nil {
			t.Fatal(err)
		}
		if _, _, err = pair[0].WriteMsgUnix([]byte("fd bytes"), unix.UnixRights(int(file.Fd())), nil); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 128)
		oob := make([]byte, unix.CmsgSpace(4))
		_ = pair[1].SetReadDeadline(time.Now().Add(2 * time.Second))
		n, on, _, _, err := pair[1].ReadMsgUnix(buf, oob)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf[:n]) != "fd bytes" {
			t.Fatal(string(buf[:n]))
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:on])
		if err != nil || len(msgs) != 1 {
			t.Fatal(msgs, err)
		}
		fds, err := unix.ParseUnixRights(&msgs[0])
		if err != nil || len(fds) != 1 {
			t.Fatal(fds, err)
		}
		received := os.NewFile(uintptr(fds[0]), "received")
		data := make([]byte, 13)
		_, err = received.ReadAt(data, 0)
		received.Close()
		file.Close()
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if string(data) != "shared buffer" {
			t.Fatal(string(data))
		}
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop")
	}
}
func TestRelayShortPayloadReads(t *testing.T) {
	client, a := unixPair(t)
	b, server := unixPair(t)
	go Relay(a, b)
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	go func() { _, _ = client.Write(payload); client.CloseWrite() }()
	got, err := io.ReadAll(server)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %d bytes", len(got))
	}
}
