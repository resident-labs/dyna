package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

type repoMapping struct {
	Path        string `json:"path"`
	Identity    string `json:"identity"`
	DefaultRoom string `json:"defaultRoom"`
}
type archiveMapping struct {
	Session string `json:"session"`
	Ref     string `json:"ref"`
}
type skillMapping struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Package string `json:"package,omitempty"`
}
type capturedManifest struct {
	Repositories []repoMapping    `json:"repositories"`
	ArchiveRefs  []archiveMapping `json:"archiveRefs"`
	Skills       []skillMapping   `json:"skills"`
}
type capture struct {
	Repos    map[string]contract.RepoName
	Defaults map[contract.RepoName]contract.RoomName
	Refs     map[string]string
	Skills   []skillMapping
}

var errUnsupportedForge = errors.New("unsupported forge")

var repoIdentityPattern = regexp.MustCompile(`^[a-zA-Z0-9.-]+(:[0-9]+)?/[a-zA-Z0-9._-]+/[a-zA-Z0-9._/-]+$`)
var sessionIDPattern = regexp.MustCompile(`^ses_[a-zA-Z0-9_-]+$`)
var skillPackagePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9._-]+$`)

func readRepoManifest(file string) (capture, error) {
	f, err := os.Open(file)
	if err != nil {
		return capture{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var manifest capturedManifest
	if err := dec.Decode(&manifest); err != nil {
		return capture{}, fmt.Errorf("decode capture manifest: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return capture{}, fmt.Errorf("capture manifest has trailing data")
	}
	if manifest.Repositories == nil || manifest.ArchiveRefs == nil || manifest.Skills == nil {
		return capture{}, fmt.Errorf("capture manifest needs repositories, archiveRefs and skills")
	}
	result := capture{Repos: make(map[string]contract.RepoName), Defaults: make(map[contract.RepoName]contract.RoomName), Refs: make(map[string]string)}
	for _, entry := range manifest.Repositories {
		if !filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path || (entry.Identity != "" && (!repoIdentityPattern.MatchString(entry.Identity) || path.Clean(entry.Identity) != entry.Identity || room.ValidateName(entry.DefaultRoom) != nil)) || (entry.Identity == "" && entry.DefaultRoom != "") {
			return capture{}, fmt.Errorf("invalid repository manifest entry")
		}
		if _, ok := result.Repos[entry.Path]; ok {
			return capture{}, fmt.Errorf("duplicate repository path in manifest")
		}
		if entry.Identity == "" {
			result.Repos[entry.Path] = ""
			continue
		}
		id, target := contract.RepoName(entry.Identity), contract.RoomName(entry.DefaultRoom)
		if prior, ok := result.Defaults[id]; ok && prior != target {
			return capture{}, fmt.Errorf("conflicting repository defaults")
		}
		result.Repos[entry.Path] = id
		result.Defaults[id] = target
	}
	for _, entry := range manifest.ArchiveRefs {
		if !sessionIDPattern.MatchString(entry.Session) || entry.Ref != "refs/dyna/archive/"+entry.Session {
			return capture{}, fmt.Errorf("invalid archive ref entry")
		}
		if _, ok := result.Refs[entry.Session]; ok {
			return capture{}, fmt.Errorf("duplicate archive session in manifest")
		}
		result.Refs[entry.Session] = entry.Ref
	}
	names := make(map[string]bool)
	for _, skill := range manifest.Skills {
		if skill.Name == "" || names[skill.Name] || (skill.Path == "") == (skill.Package == "") {
			return capture{}, fmt.Errorf("invalid or duplicate skill")
		}
		if skill.Package != "" && !skillPackagePattern.MatchString(skill.Package) {
			return capture{}, fmt.Errorf("invalid skill package reference")
		}
		if skill.Path != "" && (!filepath.IsLocal(skill.Path) || filepath.Base(skill.Path) != "SKILL.md") {
			return capture{}, fmt.Errorf("invalid captured skill path")
		}
		names[skill.Name] = true
		result.Skills = append(result.Skills, skill)
	}
	return result, nil
}

func githubRepo(identity string) (contract.RepoName, bool) {
	host, rest, ok := strings.Cut(identity, "/")
	if !ok || !strings.EqualFold(host, "github.com") {
		return "", false
	}
	rest = strings.TrimSuffix(rest, ".git")
	if strings.Count(rest, "/") != 1 || !repoIdentityPattern.MatchString("github.com/"+rest) {
		return "", false
	}
	return contract.RepoName(rest), true
}

func repoName(manifest map[string]contract.RepoName, source string) (contract.RepoName, error) {
	if identity, ok := manifest[source]; ok {
		if name, ok := githubRepo(string(identity)); ok {
			return name, nil
		}
		return "", errUnsupportedForge
	}
	if !filepath.IsAbs(source) && repoIdentityPattern.MatchString(source) && path.Clean(source) == source {
		if name, ok := githubRepo(source); ok {
			return name, nil
		}
		return "", errUnsupportedForge
	}
	return "", errUnsupportedForge
}
