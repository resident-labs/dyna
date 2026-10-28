package ghclient

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/proc"
	"dyna-agent/internal/room"
)

// Credential is the room account the engine proved. It travels only over the
// engine's private socket and into the credential-requiring child process.
type Credential struct {
	Login string `json:"login"`
	Token string `json:"token"`
}

// RoomCredential selects an account with the engine's environment, including
// token-only owner environments that have no saved gh configuration.
func RoomCredential(ctx context.Context, rec room.Record, host, name string) (Credential, error) {
	ctx = WithRepository(WithRoom(ctx, rec.ID), host, name)
	cmd := Command(ctx, "", rec.Repo)
	r := repositoryFor(ctx, cmd)
	if r.name == "" {
		return Credential{}, nil
	}
	login, token, err := selectAccount(ctx, cmd, r)
	if err != nil {
		return Credential{}, err
	}
	if token == "" {
		token = inheritedToken(cmd.Env, r.host)
		if token != "" && login == "" {
			login = "oauth2"
		}
	}
	return Credential{Login: login, Token: token}, nil
}

func engineCredential(ctx context.Context, r repository) (Credential, error) {
	var credential Credential
	err := engineclient.New("", engineclient.Notices(nil), engineclient.RestartWait(0)).Call(ctx, "GitHubCredential", []any{r.host, r.name}, &credential)
	return credential, err
}

type credentialRequest struct {
	protocol string
	host     string
	path     string
}

// SessionCredential answers git's credential protocol without exposing the
// owner's gh configuration or accepting credentials from the worker environment.
func SessionCredential(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) != 1 || args[0] != "get" {
		return nil
	}
	if !invokedByGit() {
		return nil
	}
	roomRef := os.Getenv("DYNA_ROOM_ID")
	if roomRef == "" {
		return nil
	}
	request, err := readCredentialRequest(in)
	if err != nil {
		return err
	}
	rec, err := room.DefaultStore().Resolve(roomRef)
	if err != nil {
		return err
	}
	ctx = WithRoom(ctx, rec.ID)
	if Access(ctx, rec) != ReadWrite {
		return nil
	}
	cmd := Command(ctx, "", rec.Repo)
	r := repositoryFor(ctx, cmd)
	if request.protocol != "https" || !strings.EqualFold(request.host, r.host) || (request.path != r.name && request.path != r.name+".git") {
		return nil
	}
	credential, err := engineCredential(ctx, r)
	if err != nil {
		return err
	}
	if credential.Login == "" || credential.Token == "" {
		return nil
	}
	_, err = fmt.Fprintf(out, "username=%s\npassword=%s\n", credential.Login, credential.Token)
	return err
}

func invokedByGit() bool {
	parent, err := proc.ReadStat("/proc", os.Getppid())
	if err != nil {
		return false
	}
	if parent.Command == "git" {
		return true
	}
	// HTTPS pushes ask through git-remote-http (comm truncates https to http).
	if parent.Command != "git-remote-http" {
		return false
	}
	grandparent, err := proc.ReadStat("/proc", parent.ParentPID)
	return err == nil && grandparent.Command == "git"
}

func readCredentialRequest(in io.Reader) (credentialRequest, error) {
	var request credentialRequest
	scanner := bufio.NewScanner(io.LimitReader(in, 64*1024))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "protocol":
			request.protocol = value
		case "host":
			request.host = value
		case "path":
			request.path = strings.TrimPrefix(value, "/")
		case "url":
			u, err := url.Parse(value)
			if err != nil {
				return credentialRequest{}, err
			}
			if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				request.protocol = ""
				continue
			}
			request.protocol, request.host, request.path = u.Scheme, u.Host, strings.TrimPrefix(u.Path, "/")
		}
	}
	return request, scanner.Err()
}
