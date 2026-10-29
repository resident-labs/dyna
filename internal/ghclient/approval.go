package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/boundedio"
)

// ClassifyForApproval validates the repository policy and derives the class
// inside the engine. The caller never supplies the class it wants.
func ClassifyForApproval(ctx context.Context, binary, dir string, args []string) (approvals.Class, error) {
	cmd := Command(ctx, binary, dir, args...)
	r := repositoryFor(ctx, cmd)
	if err := checkPolicy(ctx, dir, r, args); err != nil {
		return approvals.ClassOwner, err
	}
	rec, err := roomFor(ctx, dir, r)
	if err != nil {
		return approvals.ClassOwner, err
	}
	login, _ := AccountFor(ctx, cmd.Path, r.host, r.name)
	class := Classify(ctx, args, ClassifyInput{
		Host: r.host, Repo: r.name, Upstream: rec.UpstreamOf(dir), Login: login, APIHost: r.host,
		AuthorOf: func(ctx context.Context, ref ItemRef) (string, error) {
			return approvalAuthor(ctx, cmd.Path, dir, ref)
		},
	})
	return class, nil
}

// approvalAuthor reads only the item needed for an authorship decision. A
// failed read makes Classify choose owner; it never widens the agent class.
func approvalAuthor(ctx context.Context, binary, dir string, ref ItemRef) (string, error) {
	var args []string
	switch ref.Kind {
	case ItemIssue, ItemPR, ItemRun:
		args = []string{string(ref.Kind), "view"}
	default:
		return "", errors.New("unknown approval item kind")
	}
	if ref.Target != "" {
		args = append(args, ref.Target)
	}
	args = append(args, "--repo", ref.Host+"/"+ref.Repo, "--json", "author")
	if ref.Kind == ItemRun {
		args[len(args)-1] = "actor"
	}
	cmd := Command(ctx, binary, dir, args...)
	stdout, stderr := boundedio.New(64*1024), boundedio.New(8192)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := RunWithAccount(ctx, cmd, nil); err != nil {
		return "", err
	}
	var payload struct {
		Author struct{ Login string } `json:"author"`
		Actor  struct{ Login string } `json:"actor"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return "", err
	}
	login := payload.Author.Login
	if ref.Kind == ItemRun {
		login = payload.Actor.Login
	}
	if login == "" {
		return "", fmt.Errorf("%s %q has no author", ref.Kind, ref.Target)
	}
	return login, nil
}
