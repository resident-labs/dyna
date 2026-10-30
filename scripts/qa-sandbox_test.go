package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestQASandboxBoundary(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed")
	}
	scratch := t.TempDir()
	root := filepath.Join(scratch, "qa")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(scratch, "owner-secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	script := `import os,socket,sys
assert not os.path.exists(sys.argv[1])
s=socket.socket(); s.bind(('127.0.0.1',0)); s.listen()
c=socket.create_connection(s.getsockname()); c.close(); s.close()
s=socket.socket()
try: s.connect(('127.0.0.1',int(sys.argv[2])))
except OSError: pass
else: raise Exception('host listener reachable')
assert os.environ['DYNA_DATA_DIR']=='/qa/data'
assert os.environ['HOME']=='/qa/home'
open(os.path.join(sys.argv[3], 'result'), 'w').write('ok')
`
	cmd := exec.Command("bash", "qa-sandbox.sh", root, "/usr/bin/true", "/usr/share", "/usr/bin/true", "/usr/bin/true", "/usr/bin/true", "--", "/usr/bin/python3", "-c", script, secret, port, root)
	cmd.Env = append(os.Environ(), "DYNA_SCRATCH="+scratch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(root, "result")); err != nil || string(b) != "ok" {
		t.Fatalf("sandbox result: %q, %v", b, err)
	}
}

func TestQASandboxRequiresBubblewrap(t *testing.T) {
	cmd := exec.Command("/bin/bash", "qa-sandbox.sh")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires bubblewrap") {
		t.Fatalf("missing bubblewrap: %v, %s", err, out)
	}
}
