package engine

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	signName  = "Aria Owner"
	signEmail = "aria@example.com"
)

func signingService(t *testing.T, on bool) (*Service, contract.RoomID, contract.Principal, context.Context) {
	t.Helper()
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	s.SetGitIdentity(signName, signEmail)
	if on {
		yes := true
		if _, problem := s.RoomSet(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomSetRequest{Room: room, GitSigning: &yes}); problem != nil {
			t.Fatal(problem)
		}
	}
	return s, room, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, ctx
}

func commitPayload(committer string) []byte {
	ident := committer + " 1700000000 +0000"
	return []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor " + ident + "\ncommitter " + ident + "\n\nmessage\n")
}

func TestGitSignSignsOnlyForARoomWithSigningOnAndItsIdentity(t *testing.T) {
	s, room, worker, ctx := signingService(t, true)
	good := commitPayload(signName + " <" + signEmail + ">")
	got, problem := s.GitSign(ctx, worker, contract.GitSignRequest{Payload: good})
	if problem != nil || !strings.HasPrefix(got.Signature, "-----BEGIN SSH SIGNATURE-----\n") {
		t.Fatalf("signature: %+v %v", got, problem)
	}
	for name, tc := range map[string]struct {
		caller  contract.Principal
		payload []byte
		code    contract.ErrorCode
		say     string
	}{
		"another committer": {worker, commitPayload("Mallory <m@example.com>"), contract.ErrInvalid, signName + " <" + signEmail + ">"},
		"not an object":     {worker, []byte("any file the box wants signed\n"), contract.ErrInvalid, "commit and tag"},
		"the owner":         {contract.Principal{Kind: contract.PrincipalOwner}, good, contract.ErrForbidden, ""},
	} {
		if _, problem := s.GitSign(ctx, tc.caller, contract.GitSignRequest{Payload: tc.payload}); problem == nil || problem.Code != tc.code || !strings.Contains(problem.Message, tc.say) {
			t.Errorf("%s: %v", name, problem)
		}
	}
	// A room with an account of its own is signed as that account, and nothing else passes.
	if err := s.store.SetGitHubAccount(ctx, room, store.GitHubIdentity{Login: "octo", Name: "Octo Cat", Email: "octo@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.GitSign(ctx, worker, contract.GitSignRequest{Payload: good}); problem == nil {
		t.Error("the engine's identity is signed in a room whose identity is another account's")
	}
	if _, problem := s.GitSign(ctx, worker, contract.GitSignRequest{Payload: commitPayload("Octo Cat <octo@example.com>")}); problem != nil {
		t.Errorf("the room's account: %v", problem)
	}
}

func TestGitSignRefusesWhileTheRoomSigningIsOffAndNamesTheSetting(t *testing.T) {
	s, room, worker, ctx := signingService(t, false)
	_, problem := s.GitSign(ctx, worker, contract.GitSignRequest{Payload: commitPayload(signName + " <" + signEmail + ">")})
	if problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(problem.Message, "gitSigning") || !strings.Contains(problem.Message, "dyna room signing") {
		t.Fatalf("off: %v", problem)
	}
	hello, problem := s.BoxHello(ctx, worker, contract.BoxHelloRequest{})
	if problem != nil || hello.GitSigningKey != "" {
		t.Fatalf("hello with signing off: %q %v", hello.GitSigningKey, problem)
	}
	yes := true
	if _, problem := s.RoomSet(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomSetRequest{Room: room, GitSigning: &yes}); problem != nil {
		t.Fatal(problem)
	}
	key, problem := s.SigningKey(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SigningKeyRequest{})
	hello, problem2 := s.BoxHello(ctx, worker, contract.BoxHelloRequest{})
	if problem != nil || problem2 != nil || hello.GitSigningKey == "" || hello.GitSigningKey != key.PublicKey {
		t.Fatalf("hello with signing on: %q %v, key %q %v", hello.GitSigningKey, problem2, key.PublicKey, problem)
	}
	no := false
	if _, problem := s.RoomSet(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomSetRequest{Room: room, GitSigning: &no}); problem != nil {
		t.Fatal(problem)
	}
	if hello, _ := s.BoxHello(ctx, worker, contract.BoxHelloRequest{}); hello.GitSigningKey != "" {
		t.Fatal("hello still carries the key after signing went off")
	}
}

func TestOnlyTheOwnerSwitchesRoomSigning(t *testing.T) {
	s, room, worker, ctx := signingService(t, false)
	yes := true
	if _, problem := s.RoomSet(ctx, worker, contract.RoomSetRequest{Room: room, GitSigning: &yes}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", problem)
	}
	listed, err := s.RoomList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomListRequest{})
	if err != nil || listed.Rooms[0].GitSigning {
		t.Fatalf("room list: %+v %v", listed, err)
	}
}

// A commit made through the real signer binary, against a test engine on a box socket, verifies with git and ssh-keygen.
func TestACommitSignedThroughTheEngineVerifies(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	s, room, worker, ctx := signingService(t, true)
	record, err := s.store.Session(ctx, worker.Session)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	signer := filepath.Join(dir, "dyna")
	if out, err := exec.Command("go", "build", "-o", signer, "../cmd/dyna").CombinedOutput(); err != nil {
		t.Fatalf("build the signer: %v\n%s", err, out)
	}
	// A unix socket path is limited to about 100 bytes, which the test's temp folder can exceed.
	runtime, err := os.MkdirTemp("/tmp", "gs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	sockets := api.NewSockets(api.New(boxService{Service: s}, nil), runtime, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	if err := sockets.AddBox(record.Box, worker.Session, room); err != nil {
		t.Fatal(err)
	}
	hello, problem := s.BoxHello(ctx, worker, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	repo := filepath.Join(dir, "repo")
	env := append(os.Environ(), "HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "DYNA_SOCKET="+filepath.Join(runtime, "boxes", string(record.Box)+".sock"))
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	// The same settings the box agent writes at start, with the signer binary as the program.
	for _, entry := range [][2]string{{"user.name", hello.GitName}, {"user.email", hello.GitEmail}, {"gpg.format", "ssh"}, {"gpg.ssh.program", signer}, {"user.signingkey", "key::" + hello.GitSigningKey}, {"commit.gpgsign", "true"}, {"tag.gpgsign", "true"}} {
		git("config", "--global", entry[0], entry[1])
	}
	git("commit", "-q", "--allow-empty", "-m", "signed")
	git("tag", "-m", "release", "v1")
	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(signEmail+" "+hello.GitSigningKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Verification uses ssh-keygen: the box's program only signs.
	verify := []string{"-c", "gpg.ssh.program=" + keygen, "-c", "gpg.ssh.allowedSignersFile=" + allowed}
	if out := git(append(verify, "verify-commit", "HEAD")...); out != "" {
		t.Logf("verify-commit: %s", out)
	}
	git(append(verify, "verify-tag", "v1")...)
	if got := git(append(verify, "log", "-1", "--format=%G?")...); got != "G" {
		t.Fatalf("signature state with the allowed signers: %q", got)
	}

	// A committer that is not the configured identity is refused, and git carries the engine's text.
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", "forged")
	cmd.Dir, cmd.Env = repo, append(env, "GIT_COMMITTER_NAME=Mallory", "GIT_COMMITTER_EMAIL=m@example.com")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "the engine signs only as "+signName) {
		t.Fatalf("forged committer: %v\n%s", err, out)
	}

	// With the room's signing off, the same box is refused with the setting's name.
	no := false
	if _, problem := s.RoomSet(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomSetRequest{Room: room, GitSigning: &no}); problem != nil {
		t.Fatal(problem)
	}
	cmd = exec.Command("git", "commit", "-q", "--allow-empty", "-m", "off")
	cmd.Dir, cmd.Env = repo, env
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "gitSigning") {
		t.Fatalf("signing off: %v\n%s", err, out)
	}
}

func TestGitSignRefusesARoomThatIsBeingDeleted(t *testing.T) {
	s, room, worker, ctx := signingService(t, true)
	good := commitPayload(signName + " <" + signEmail + ">")
	if _, problem := s.GitSign(ctx, worker, contract.GitSignRequest{Payload: good}); problem != nil {
		t.Fatalf("an open room: %v", problem)
	}
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE rooms SET state='deleting' WHERE id=?", room)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := s.GitSign(ctx, worker, contract.GitSignRequest{Payload: good}); problem == nil || !strings.Contains(problem.Message, "being deleted") {
		t.Fatalf("a deleting room: %v", problem)
	}
	if key, problem := s.boxSigningKey(ctx, room); problem != nil || key != "" {
		t.Fatalf("a deleting room gets no key: %q %v", key, problem)
	}
}
