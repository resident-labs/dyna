package repoid

import (
	"net/url"
	"path/filepath"
	"strings"
)

// Transport and user names do not change a hosted repository's identity.
// Local paths also share an identity through symlinks and file URLs.
func RemoteIdentity(repo, remote string) (string, error) {
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		if u.Scheme == "file" {
			remote = u.Path
		} else {
			host = u.Hostname()
			path = u.Path
			if u.Port() != "" {
				host += ":" + u.Port()
			}
		}
	} else if strings.HasPrefix(remote, "file://") {
		remote = strings.TrimPrefix(remote, "file://")
	} else if before, after, ok := strings.Cut(remote, ":"); ok && !strings.Contains(before, "/") {
		host, path = before, after
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
	}
	if host != "" {
		host = strings.ToLower(host)
		path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
		if host == "github.com" {
			path = strings.ToLower(path)
		}
		return host + "/" + path, nil
	}
	if !filepath.IsAbs(remote) {
		remote = filepath.Join(repo, remote)
	}
	remote, err := filepath.Abs(remote)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(remote)
}

// RepoName gives GitHub SSH and HTTPS remotes the same short repository name.
func RepoName(remote string) string {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimPrefix(remote, "git@github.com:")
	if u, err := url.Parse(remote); err == nil && u.Hostname() == "github.com" {
		remote = strings.TrimPrefix(u.Path, "/")
	}
	return strings.TrimSuffix(remote, ".git")
}
