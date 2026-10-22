package callerid

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dialHelperEnv makes this test binary connect to a socket and exit.
const dialHelperEnv = "CALLERID_DIAL_HELPER"

func TestDialHelper(t *testing.T) {
	path := os.Getenv(dialHelperEnv)
	if path == "" {
		t.Skip("runs only as a child")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(3)
	}
	conn.Close()
	os.Exit(0)
}

// listen returns a Unix listener in a temporary folder.
func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, path
}

// The engine resolves the process at the other end of the socket.
func TestResolveConnReadsThePeer(t *testing.T) {
	l, path := listen(t)
	self, _, err := readStat("/proc", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	session := Identity{Kind: Session, Session: "ses_peer"}
	r, err := New("/proc", os.Getppid(), records{processes: map[Process]Identity{self: session}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if got := r.ResolveConn(server); got != session {
		t.Fatalf("ResolveConn = %+v, want the peer's session", got)
	}
	if got := r.ResolveConn(client.(*net.UnixConn)); got.Kind != Session {
		t.Fatalf("either end of the socket names this process: %+v", got)
	}
}

// A peer that is gone before the engine reads it is refused: its PID may
// already name another process.
func TestResolveConnRefusesAGonePeer(t *testing.T) {
	l, path := listen(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDialHelper$")
	cmd.Env = append(os.Environ(), dialHelperEnv+"="+path)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	server, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	r, err := New("/proc", os.Getpid(), records{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.ResolveConn(server); got.Kind != Refused {
		t.Fatalf("a gone peer = %+v, want refused", got)
	}
}

// Nothing inside an agent's computer calls the engine (perf-lead's decision
// on item 3), so a process under the computers unit is refused by rule (b),
// and the reason names the unit.
func TestComputersUnitIsRefused(t *testing.T) {
	r, proc := newResolver(t, engineCG)
	proc.add(800, 1, 80, user+"/dyna.slice/dyna-computers.service/ses_20261020-000000-0000comp")
	proc.add(801, 1, 81, user+"/dyna.slice")
	for pid, want := range map[int]string{800: "dyna-computers.service", 801: "dyna.slice"} {
		got := r.Resolve(pid)
		if got.Kind != Refused || !strings.Contains(got.Reason, "rule (b)") || !strings.Contains(got.Reason, want) {
			t.Fatalf("Resolve(%d) = %+v, want a rule (b) refusal naming %s", pid, got, want)
		}
	}
}
