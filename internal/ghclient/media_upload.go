package ghclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/repoid"
)

type approvedMediaKey struct{}

// WithApprovedMedia binds execution to the destination recorded before approval.
func WithApprovedMedia(ctx context.Context, destination approvals.MediaDestination) context.Context {
	return context.WithValue(ctx, approvedMediaKey{}, destination)
}

// ResolveMediaDestination resolves and checks the repositories with the selected account.
func ResolveMediaDestination(ctx context.Context, cmd *exec.Cmd) (approvals.MediaDestination, error) {
	r := repositoryFor(ctx, cmd)
	if r.host == "" || r.name == "" {
		return approvals.MediaDestination{}, errors.New("media requires an unambiguous base repository")
	}
	if err := checkPolicy(ctx, cmd.Dir, r, cmd.Args[1:]); err != nil {
		return approvals.MediaDestination{}, err
	}
	_, token, err := selectAccountAfterPolicy(ctx, cmd, r)
	if err != nil {
		return approvals.MediaDestination{}, err
	}
	return resolveMediaDestination(ctx, cmd, r, token)
}

func resolveMediaDestination(ctx context.Context, cmd *exec.Cmd, r repository, token string) (approvals.MediaDestination, error) {
	args := commandArgs(cmd.Args[1:])
	if len(args) < 2 || args[0] != "pr" || (args[1] != "create" && args[1] != "comment") {
		return approvals.MediaDestination{}, errors.New("media is supported only for PR create and comment")
	}
	p := parseArgs(args[:2], args[2:])
	head := ""
	number := 0
	if args[1] == "comment" {
		if len(p.positional) != 1 {
			return approvals.MediaDestination{}, errors.New("media requires an explicit PR number")
		}
		targetText := p.positional[0]
		if u, parseErr := url.Parse(targetText); parseErr == nil && u.Scheme == "https" && strings.EqualFold(u.Host, r.host) && u.RawQuery == "" && u.Fragment == "" {
			prefix := "/" + r.name + "/pull/"
			if len(u.Path) >= len(prefix) && strings.EqualFold(u.Path[:len(prefix)], prefix) {
				targetText = u.Path[len(prefix):]
			}
		}
		target, err := strconv.Atoi(targetText)
		if err != nil || target <= 0 {
			return approvals.MediaDestination{}, errors.New("media requires an explicit PR number")
		}
		view := []string{"pr", "view"}
		if len(p.positional) == 1 {
			view = append(view, p.positional[0])
		}
		view = append(view, "--repo", r.host+"/"+r.name, "--json", "headRepository,headRepositoryOwner,number")
		raw, err := authOutput(ctx, cmd, token, view...)
		if err != nil {
			return approvals.MediaDestination{}, fmt.Errorf("cannot resolve PR head repository: %w", err)
		}
		var pr struct {
			Number         int `json:"number"`
			HeadRepository struct {
				Name string `json:"name"`
			} `json:"headRepository"`
			HeadRepositoryOwner struct {
				Login string `json:"login"`
			} `json:"headRepositoryOwner"`
		}
		if err := json.Unmarshal(raw, &pr); err != nil {
			return approvals.MediaDestination{}, err
		}
		if pr.Number != target || pr.HeadRepository.Name == "" || pr.HeadRepositoryOwner.Login == "" {
			return approvals.MediaDestination{}, errors.New("PR head repository is unavailable")
		}
		number = pr.Number
		head = pr.HeadRepositoryOwner.Login + "/" + pr.HeadRepository.Name
	} else {
		remote, err := repoid.Git(ctx, cmd.Dir, "remote", "get-url", "origin")
		if err != nil {
			return approvals.MediaDestination{}, errors.New("media requires a known origin repository")
		}
		identity, err := repoid.RemoteIdentity(cmd.Dir, remote)
		if err != nil {
			return approvals.MediaDestination{}, errors.New("media requires a known origin repository")
		}
		parts := strings.Split(identity, "/")
		if len(parts) != 3 || !strings.EqualFold(parts[0], r.host) {
			return approvals.MediaDestination{}, errors.New("media origin does not match the base host")
		}
		head = parts[1] + "/" + parts[2]
		requested := p.flags["--head"]
		if requested == "" {
			requested = p.flags["-H"]
		}
		if requested == "" {
			return approvals.MediaDestination{}, errors.New("media requires an explicit PR head")
		}
		owner, branch, qualified := strings.Cut(requested, ":")
		if qualified && (!strings.EqualFold(owner, parts[1]) || branch == "") {
			return approvals.MediaDestination{}, errors.New("PR head does not match origin")
		}
		if !qualified && !strings.EqualFold(head, r.name) {
			return approvals.MediaDestination{}, errors.New("fork media requires an owner-qualified PR head")
		}
	}
	var visibility approvals.MediaVisibility
	for index, name := range []string{r.name, head} {
		raw, err := authOutput(ctx, cmd, token, "api", "--hostname", r.host, "repos/"+name)
		if err != nil {
			return approvals.MediaDestination{}, fmt.Errorf("cannot inspect media repository visibility: %w", err)
		}
		var repo struct {
			FullName   string                    `json:"full_name"`
			Private    *bool                     `json:"private"`
			Visibility approvals.MediaVisibility `json:"visibility"`
		}
		if err := json.Unmarshal(raw, &repo); err != nil {
			return approvals.MediaDestination{}, err
		}
		if repo.Private == nil || !strings.EqualFold(repo.FullName, name) ||
			(repo.Visibility != approvals.MediaVisibilityPublic && repo.Visibility != approvals.MediaVisibilityPrivate && repo.Visibility != approvals.MediaVisibilityInternal) ||
			*repo.Private != (repo.Visibility != approvals.MediaVisibilityPublic) {
			return approvals.MediaDestination{}, errors.New("repository visibility is unknown")
		}
		if index == 0 {
			visibility = repo.Visibility
		} else {
			if repo.Visibility != visibility {
				return approvals.MediaDestination{}, errors.New("base and head repository visibility differs")
			}
			if repo.Visibility != approvals.MediaVisibilityPublic && !strings.EqualFold(r.name, head) {
				return approvals.MediaDestination{}, errors.New("private fork media cannot guarantee reviewer access")
			}
		}
	}
	return approvals.MediaDestination{Host: r.host, Base: r.name, Head: head, Number: number, Visibility: visibility}, nil
}

// stripMedia removes only attachment flags from an approved PR command.
func stripMedia(ctx context.Context, cmd *exec.Cmd) ([]string, error) {
	args := commandArgs(cmd.Args[1:])
	if len(args) < 2 || args[0] != "pr" || (args[1] != "create" && args[1] != "comment") {
		return nil, nil
	}
	start := len(cmd.Args) - len(args) + 2
	rest := append([]string(nil), cmd.Args[:start]...)
	var paths []string
	for i := start; i < len(cmd.Args); i++ {
		arg := cmd.Args[i]
		if arg == "--" {
			rest = append(rest, cmd.Args[i:]...)
			break
		}
		name, value, has := strings.Cut(arg, "=")
		if name != "--attach" {
			rest = append(rest, arg)
			if repositoryFlagTakesValue(args[:2], name) && !has && i+1 < len(cmd.Args) {
				i++
				rest = append(rest, cmd.Args[i])
			}
			continue
		}
		if !has {
			i++
			if i >= len(cmd.Args) {
				return nil, errors.New("--attach requires a path")
			}
			value = cmd.Args[i]
		}
		if value == "" {
			return nil, errors.New("--attach requires a path")
		}
		paths = append(paths, value)
	}
	if len(paths) > 0 {
		if _, ok := ctx.Value(approvedMediaKey{}).(approvals.MediaDestination); !ok {
			return nil, errors.New("media requires approval")
		}
		cmd.Args = rest
	}
	return paths, nil
}

func uploadMedia(ctx context.Context, cmd *exec.Cmd, r repository, token string, paths []string) (func(), error) {
	none := func() {}
	if len(paths) == 0 {
		return none, nil
	}
	approved := ctx.Value(approvedMediaKey{}).(approvals.MediaDestination)
	current, err := resolveMediaDestination(ctx, cmd, r, token)
	if err != nil {
		return none, err
	}
	if !strings.EqualFold(approved.Host, current.Host) || !strings.EqualFold(approved.Base, current.Base) || !strings.EqualFold(approved.Head, current.Head) || approved.Number != current.Number || approved.Visibility != current.Visibility {
		return none, errors.New("media destination changed since approval")
	}
	type media struct {
		path, name string
		image      bool
	}
	var files []media
	dir, err := os.MkdirTemp("", "dyna-pr-media-")
	if err != nil {
		return none, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			cleanup()
			return none, err
		}
		if len(data) > maxMediaSize {
			cleanup()
			return none, errors.New("media exceeds 25 MiB")
		}
		ext := strings.ToLower(filepath.Ext(path))
		sum := sha256.Sum256(data)
		name := fmt.Sprintf("%x%s", sum, ext)
		target := filepath.Join(dir, name)
		if err := os.WriteFile(target, data, 0600); err != nil {
			cleanup()
			return none, err
		}
		files = append(files, media{target, name, ext != ".mp4" && ext != ".webm"})
	}
	repo := current.Host + "/" + current.Head
	endpoint := "repos/" + current.Head + "/releases/tags/dyna-pr-media"
	raw, stderr, err := authResult(ctx, cmd, token, "api", "--hostname", current.Host, endpoint)
	if err != nil {
		if !strings.Contains(string(stderr), "HTTP 404") {
			cleanup()
			return none, errors.New("cannot read media release; check releases permission")
		}
		_, err = authOutput(ctx, cmd, token, "release", "create", "dyna-pr-media", "--repo", repo, "--prerelease", "--title", "Dyna PR media", "--notes", "Media attached to pull requests by Dyna.")
		if err != nil {
			createErr := err
			raw, err = authOutput(ctx, cmd, token, "api", "--hostname", current.Host, endpoint)
			if err != nil {
				cleanup()
				return none, fmt.Errorf("cannot create media release; releases write permission is required: %w", createErr)
			}
		} else {
			raw = []byte(`{"assets":[]}`)
		}
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &release); err != nil {
		cleanup()
		return none, err
	}
	assets := map[string]string{}
	for _, a := range release.Assets {
		assets[a.Name] = a.Digest
	}
	var links []string
	for _, file := range files {
		u, exists := assets[file.name]
		if !exists {
			_, uploadErr := authOutput(ctx, cmd, token, "release", "upload", "dyna-pr-media", file.path, "--repo", repo)
			raw, err = authOutput(ctx, cmd, token, "api", "--hostname", current.Host, endpoint)
			if err != nil {
				cleanup()
				if uploadErr != nil {
					return none, fmt.Errorf("media upload failed after %d successful or reused assets; releases write permission is required: %w", len(links), uploadErr)
				}
				return none, fmt.Errorf("media upload succeeded but asset digest could not be read: %w", err)
			}
			if err = json.Unmarshal(raw, &release); err != nil {
				cleanup()
				return none, err
			}
			for _, a := range release.Assets {
				assets[a.Name] = a.Digest
			}
			u = assets[file.name]
			if uploadErr != nil && u != "sha256:"+strings.TrimSuffix(file.name, filepath.Ext(file.name)) {
				cleanup()
				return none, fmt.Errorf("media upload failed after %d successful or reused assets; releases write permission is required or the existing asset digest does not match: %w", len(links), uploadErr)
			}
			if u == "" {
				cleanup()
				return none, errors.New("uploaded media asset digest is missing")
			}
		}
		if u != "sha256:"+strings.TrimSuffix(file.name, filepath.Ext(file.name)) {
			cleanup()
			return none, fmt.Errorf("media asset digest mismatch or unavailable after %d successful or reused assets", len(links))
		}
		prefix := ""
		if file.image {
			prefix = "!"
		}
		download := "https://" + current.Host + "/" + current.Head + "/releases/download/dyna-pr-media/" + file.name
		links = append(links, prefix+"[Media]("+download+")")
	}
	args := cmd.Args
	start := len(args) - len(commandArgs(args[1:])) + 2
	rest := append([]string(nil), args[:start]...)
	body := ""
	for i := start; i < len(args); i++ {
		name, value, has := strings.Cut(args[i], "=")
		if flag, v, ok := shortFlag(args[i]); ok {
			name, value, has = flag, strings.TrimPrefix(v, "="), true
		}
		if name == "--body" || name == "-b" || name == "--body-file" || name == "-F" {
			if !has {
				i++
				if i >= len(args) {
					cleanup()
					return none, errors.New("body flag requires a value")
				}
				value = args[i]
			}
			if name == "--body-file" || name == "-F" {
				data, e := readBodyFile(cmd, value)
				if e != nil {
					cleanup()
					return none, e
				}
				value = string(data)
			}
			body = value
			continue
		}
		rest = append(rest, args[i])
		if repositoryFlagTakesValue([]string{"pr", commandArgs(args[1:])[1]}, name) && !has && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	body = strings.TrimRight(body, "\n") + "\n\n" + strings.Join(links, "\n") + "\n"
	bodyPath := filepath.Join(dir, "body.md")
	if err := os.WriteFile(bodyPath, []byte(body), 0600); err != nil {
		cleanup()
		return none, err
	}
	cmd.Args = append(rest, "--body-file", bodyPath)
	return cleanup, nil
}
