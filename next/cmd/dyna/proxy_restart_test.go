package main

import (
	"context"
	"net/rpc"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	proxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestFakeProxyChild is invoked in a separate process by TestProxyStoreSurvivesFakeChildRestart.
func TestFakeProxyChild(t *testing.T) {
	mode := os.Getenv("DYNA_FAKE_PROXY_CHILD")
	if mode == "" {
		return
	}
	client := rpc.NewClient(os.NewFile(3, "private-store"))
	defer client.Close()
	p := &proxyStore{client: client}
	if mode == "create" {
		auth := &proxyauth.Auth{ID: "invented", Provider: "claude", Storage: &inventedToken{AccessToken: "invented-token"}}
		if _, err := p.Save(proxyauth.WithAuthCreationIntent(context.Background()), auth); err != nil {
			t.Fatal("save failed")
		}
		return
	}
	mgr := proxyauth.NewManager(p, nil, nil)
	if err := mgr.Load(context.Background()); err != nil {
		t.Fatal("load failed")
	}
	auths := mgr.List()
	if len(auths) != 1 || auths[0].Metadata["access_token"] != "invented-token" {
		t.Fatal("restart did not adopt credential")
	}
}

func TestProxyStoreSurvivesFakeChildRestart(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, mode := range []string{"create", "restart"} {
		if mode == "restart" {
			if err := st.BindAccount(context.Background(), "account", "invented", "anthropic", "Invented"); err != nil {
				t.Fatal(err)
			}
			if err := st.PruneUnboundProxyAuths(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		server, child := os.NewFile(uintptr(fds[0]), "server"), os.NewFile(uintptr(fds[1]), "child")
		r := rpc.NewServer()
		if err := r.RegisterName("Auth", &model.AuthRPC{Store: st}); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { r.ServeConn(server); close(done) }()
		cmd := exec.Command(os.Args[0], "-test.run=^TestFakeProxyChild$")
		cmd.Env = append(os.Environ(), "DYNA_FAKE_PROXY_CHILD="+mode)
		cmd.ExtraFiles = []*os.File{child}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		child.Close()
		err = cmd.Wait()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		server.Close()
		<-done
		if err != nil {
			t.Fatal("fake proxy child failed")
		}
	}
}
