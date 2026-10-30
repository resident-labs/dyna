package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/boundedio"
)

// ClassifyForApproval validates the repository policy and derives the class
// inside the engine. The caller never supplies the class it wants.
func ClassifyForApproval(ctx context.Context, binary, dir string, args []string) (approvals.Class, error) {
	class, _, err := classifyForApproval(ctx, binary, dir, args, nil)
	return class, err
}

// ClassifyForSession adds engine-verified session PR ownership.
func ClassifyForSession(ctx context.Context, binary, dir string, args []string, owns func(context.Context, ItemRef) bool) (approvals.Class, []string, error) {
	return classifyForApproval(ctx, binary, dir, args, owns)
}

func classifyForApproval(ctx context.Context, binary, dir string, args []string, owns func(context.Context, ItemRef) bool) (approvals.Class, []string, error) {
	cmd := Command(ctx, binary, dir, args...)
	r := repositoryFor(ctx, cmd)
	if isReadCommand(commandArgs(args)) {
		if err := checkPolicy(ctx, dir, r, args); err != nil {
			return approvals.ClassOwner, nil, err
		}
		return approvals.ClassRead, args, nil
	}
	w, decision, err := decideCommand(ctx, cmd, r, "", owns)
	if err != nil {
		return approvals.ClassOwner, nil, err
	}
	if decision.Class == approvals.ClassAuto && w.Verb == "pr.create" && commandArgs(args)[0] != "api" {
		if slices.Contains(args, "--") {
			return approvals.ClassAgent, args, nil
		}
		args = append(slices.Clone(args), "--repo", r.host+"/"+r.name)
	}
	return decision.Class, args, nil
}

// decideCommand binds repository and account state to the common write policy.
func decideCommand(ctx context.Context, cmd *exec.Cmd, r repository, login string, owns func(context.Context, ItemRef) bool) (Write, Decision, error) {
	rec, err := roomFor(ctx, cmd.Dir, r)
	target := TargetRoom
	if errors.Is(err, errNotInRoom) {
		target = TargetOther
		if matchesUpstream(rec, r) || matchesForkParent(ctx, rec, cmd.Dir, r) {
			target = TargetUpstream
		}
	} else if err != nil {
		return Write{}, Decision{}, err
	}
	w := ParseWrite(cmd.Args[1:], r.host, r.name, target)
	if login == "" && w.ValidItem && slices.Contains([]Verb{"issue.close", "issue.reopen", "pr.close", "pr.reopen", "comment.edit", "comment.delete", "run.rerun", "run.rerun.failed", "run.cancel"}, w.Verb) {
		write := target == TargetRoom
		if target == TargetUpstream {
			_, comment := ownCommentCall(cmd.Args[1:])
			write = isRepoFork(cmd.Args[1:]) || comment
		}
		login, _, _ = selectAccountForRoom(ctx, cmd, r, rec, write)
	}
	lookups := WriteLookups{Login: login, OwnPR: owns,
		AuthorOf: func(ctx context.Context, ref ItemRef) (string, error) {
			return approvalAuthor(ctx, cmd.Path, cmd.Dir, ref)
		},
		ForkHead: func(ctx context.Context, w Write) bool {
			args := commandArgs(w.Argv)
			if len(args) > 0 && args[0] == "api" {
				args = []string{"pr", "edit", w.Ref.Target}
			}
			return editsForkPull(ctx, rec, cmd.Dir, r, args)
		},
	}
	d, err := Decide(ctx, rec, w, lookups)
	if err != nil {
		return w, d, err
	}
	if d.Refusal != "" {
		return w, d, &RefusalError{Write: w, Decision: d}
	}
	if err := refuseImplicitForkPR(rec, cmd.Dir, r, w.Argv); err != nil {
		return w, d, err
	}
	if err := refuseHeldReady(ctx, rec, cmd.Dir, r, w.Argv); err != nil {
		return w, d, err
	}
	return w, d, nil
}

// approvalAuthor reads only the item needed for an authorship decision. A
// failed read makes Classify choose owner; it never widens the agent class.
func approvalAuthor(ctx context.Context, binary, dir string, ref ItemRef) (string, error) {
	var args []string
	if ref.Kind == ItemComment {
		args = []string{"api", "--hostname", ref.Host, "repos/" + ref.Repo + "/" + ref.Target}
	} else {
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
		User   struct{ Login string } `json:"user"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return "", err
	}
	login := payload.Author.Login
	if ref.Kind == ItemRun {
		login = payload.Actor.Login
	}
	if ref.Kind == ItemComment {
		login = payload.User.Login
	}
	if login == "" {
		return "", fmt.Errorf("%s %q has no author", ref.Kind, ref.Target)
	}
	return login, nil
}

// ApprovalPRNumber resolves a target to a PR in the repository being checked.
func ApprovalPRNumber(ctx context.Context, dir string, ref ItemRef) (int, error) {
	args := []string{"pr", "view"}
	if ref.Target != "" {
		args = append(args, ref.Target)
	}
	args = append(args, "--repo", ref.Host+"/"+ref.Repo, "--json", "number,url")
	cmd := Command(ctx, "", dir, args...)
	stdout := boundedio.New(64 * 1024)
	cmd.Stdout = stdout
	if err := RunWithAccount(ctx, cmd, nil); err != nil {
		return 0, err
	}
	var result struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return 0, err
	}
	u, err := url.Parse(result.URL)
	if err != nil || !strings.EqualFold(u.Hostname(), ref.Host) || !strings.EqualFold(strings.Trim(u.Path, "/"), ref.Repo+"/pull/"+strconv.Itoa(result.Number)) || result.Number <= 0 {
		return 0, errors.New("pull request does not match repository")
	}
	return result.Number, nil
}

// PinRoutinePR replaces the mutable branch target with the checked PR number.
func PinRoutinePR(args []string, ref ItemRef, number int) []string {
	command := commandArgs(args)
	start := len(args) - len(command)
	out := append([]string(nil), args[:start+2]...)
	out = append(out, strconv.Itoa(number))
	rest := args[start+2:]
	// Remove the one target while retaining flag values and their order.
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		out = append(out, arg)
		if strings.HasPrefix(arg, "--") {
			flag, _, attached := strings.Cut(arg, "=")
			if !attached && (flag == "--repo" || repositoryFlagTakesValue(command[:2], flag)) && i+1 < len(rest) {
				i++
				out = append(out, rest[i])
			}
		} else {
			for j := 1; j < len(arg); j++ {
				flag := "-" + arg[j:j+1]
				if flag == "-R" || repositoryFlagTakesValue(command[:2], flag) {
					if j+1 == len(arg) && i+1 < len(rest) {
						i++
						out = append(out, rest[i])
					}
					break
				}
			}
		}
	}
	return append(out, "--repo", ref.Host+"/"+ref.Repo)
}
