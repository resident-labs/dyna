package addons

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box/skills"
)

var (
	npmName    = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	npmVersion = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$`)
	gitCommit  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	gitSCP     = regexp.MustCompile(`^git@([^:/]+):(.+)$`)
)

// Sources reads what a source names, at add time, so that every box later gets the same thing.
type Sources struct {
	Registry string // the npm registry
	GitHub   string // where GitHub serves a repository's tarball
	HTTP     *http.Client
	Git      func(ctx context.Context, args ...string) ([]byte, error)
}

// NewSources reaches the real registry and GitHub.
func NewSources() *Sources {
	return &Sources{Registry: "https://registry.npmjs.org", GitHub: "https://codeload.github.com", HTTP: &http.Client{Timeout: time.Minute}, Git: func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "git", args...).Output()
	}}
}

// Resolve parses a Pi package source as Pi does and pins it. It returns the name Pi knows the package by: the npm name, or
// the host and path it clones to. Pi takes a version range as it finds it, so a range cannot be pinned and is refused.
func (s *Sources) Resolve(ctx context.Context, source string) (string, string, error) {
	source = strings.TrimSpace(source)
	if spec, ok := strings.CutPrefix(source, "npm:"); ok {
		return s.resolveNPM(ctx, spec)
	}
	name, base, repo, ref, ok := parseGit(source)
	if !ok {
		return "", "", fmt.Errorf("%q is not a Pi package source: use npm:NAME, git:HOST/OWNER/REPO or a repository URL", source)
	}
	if !gitCommit.MatchString(ref) {
		commit, err := s.commit(ctx, repo, ref)
		if err != nil {
			return "", "", err
		}
		ref = commit
	}
	return name, base + "@" + ref, nil
}

func (s *Sources) resolveNPM(ctx context.Context, spec string) (string, string, error) {
	name, version := spec, ""
	if at := strings.LastIndexByte(spec, '@'); at > 0 {
		name, version = spec[:at], spec[at+1:]
	}
	if !npmName.MatchString(name) {
		return "", "", fmt.Errorf("%q is not an npm package name", name)
	}
	if npmVersion.MatchString(version) {
		return name, "npm:" + name + "@" + version, nil
	}
	if version == "" {
		version = "latest"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Registry+"/-/package/"+url.PathEscape(name)+"/dist-tags", nil)
	if err != nil {
		return "", "", err
	}
	response, err := s.HTTP.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("the npm registry did not answer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the npm registry has no package %s (%s)", name, response.Status)
	}
	var tags map[string]string
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tags); err != nil {
		return "", "", fmt.Errorf("the npm registry's answer for %s: %w", name, err)
	}
	pinned := tags[version]
	if pinned == "" {
		return "", "", fmt.Errorf("npm package %s has no tag %q; give an exact version or a tag", name, version)
	}
	return name, "npm:" + name + "@" + pinned, nil
}

// parseGit splits a git source. name is Pi's folder for it, the host and the path; base is the source without its ref.
func parseGit(source string) (name, base, repo, ref string, ok bool) {
	text, prefixed := strings.CutPrefix(source, "git:")
	var host, path string
	switch match := gitSCP.FindStringSubmatch(text); {
	case match != nil:
		host, path = match[1], match[2]
		repo = "git@" + host + ":"
		base = "git:" + repo
	case strings.Contains(text, "://"):
		parsed, err := url.Parse(text)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "ssh" && parsed.Scheme != "git") {
			return
		}
		host, path = parsed.Hostname(), strings.TrimPrefix(parsed.Path, "/")
		parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "/", "", "", ""
		repo = parsed.String()
		base = repo
		if prefixed {
			base = "git:" + repo
		}
	case prefixed:
		var found bool
		if host, path, found = strings.Cut(text, "/"); !found || !strings.Contains(host, ".") {
			return
		}
		repo = "https://" + host + "/"
		base = "git:" + host + "/"
	default:
		return
	}
	path, ref, _ = strings.Cut(path, "@")
	trimmed := strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if host == "" || strings.Count(trimmed, "/") < 1 || strings.Contains(trimmed, "..") {
		return
	}
	return host + "/" + trimmed, base + path, repo + path, ref, true
}

// commit asks the remote what a ref, or its default branch, points at. A tag's peeled line is the commit.
func (s *Sources) commit(ctx context.Context, repo, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	args := []string{"ls-remote", "--", repo, "HEAD"}
	if ref != "" {
		args = []string{"ls-remote", "--", repo, ref, ref + "^{}"}
	}
	out, err := s.Git(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("git could not read %s: %w", repo, err)
	}
	var commit string
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && gitCommit.MatchString(fields[0]) && (commit == "" || strings.HasSuffix(fields[1], "^{}")) {
			commit = fields[0]
		}
	}
	if commit == "" {
		return "", fmt.Errorf("%s has no ref %q", repo, ref)
	}
	return commit, nil
}

// Skill fetches a link into a skill archive. A GitHub link to a repository or a folder brings that whole folder, and any
// other link must be a SKILL.md, the one file it can name.
func (s *Sources) Skill(ctx context.Context, link string) ([]byte, error) {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	if parsed.Host == "github.com" {
		return s.githubSkill(ctx, strings.Split(strings.Trim(parsed.Path, "/"), "/"))
	}
	body, closeBody, err := s.get(ctx, link)
	if err != nil {
		return nil, err
	}
	defer closeBody()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, err
	}
	if _, err := skills.Parse(data); err != nil {
		return nil, fmt.Errorf("%s is not a SKILL.md: %w", link, err)
	}
	archive := skills.NewArchive()
	if err := archive.Add(skills.File, 0o600, data); err != nil {
		return nil, err
	}
	return archive.Bytes()
}

func (s *Sources) get(ctx context.Context, link string) (io.Reader, func() error, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := s.HTTP.Do(request)
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, nil, fmt.Errorf("%s: %s", link, response.Status)
	}
	return response.Body, response.Body.Close, nil
}

// githubSkill reads one folder out of a repository's tarball. A branch with a slash in its name cannot be told from a
// path in a link, so such a link fails on the missing folder.
func (s *Sources) githubSkill(ctx context.Context, parts []string) ([]byte, error) {
	if len(parts) < 2 {
		return nil, errors.New("a GitHub link must name a repository")
	}
	ref, folder := "HEAD", ""
	if len(parts) > 3 && (parts[2] == "tree" || parts[2] == "blob") {
		ref = parts[3]
		path := parts[4:]
		if len(path) > 0 && path[len(path)-1] == skills.File {
			path = path[:len(path)-1]
		}
		if folder = strings.Join(path, "/"); folder != "" {
			folder += "/"
		}
	}
	body, closeBody, err := s.get(ctx, s.GitHub+"/"+parts[0]+"/"+strings.TrimSuffix(parts[1], ".git")+"/tar.gz/"+ref)
	if err != nil {
		return nil, err
	}
	defer closeBody()
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, err
	}
	reader, archive, found, total := tar.NewReader(gz), skills.NewArchive(), false, int64(0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// Every name starts with one folder that GitHub adds.
		_, name, _ := strings.Cut(header.Name, "/")
		rel, ok := strings.CutPrefix(name, folder)
		if !ok || header.Typeflag != tar.TypeReg {
			continue
		}
		if total += header.Size; total > skills.MaxBytes {
			return nil, fmt.Errorf("that folder holds more than %d MiB", skills.MaxBytes>>20)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		if err := archive.Add(rel, header.FileInfo().Mode(), data); err != nil {
			return nil, err
		}
		found = found || rel == skills.File
	}
	if !found {
		return nil, fmt.Errorf("%s/%s has no %s in %q", parts[0], parts[1], skills.File, strings.TrimSuffix(folder, "/"))
	}
	return archive.Bytes()
}
