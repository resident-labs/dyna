package settings

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// KeyLibraryEnabled lists the repositories whose librarian runs.
	KeyLibraryEnabled = "library.enabled"
	// KeyLibraryProfile is the profile for feature map and docs work; empty
	// ranks enabled service profiles.
	KeyLibraryProfile = "library.profile"
	// KeyLibrarySmellProfile is the profile for code-reading smell passes;
	// empty ranks profiles with the reviewer's diagnostics preference.
	KeyLibrarySmellProfile = "library.smellProfile"
	// KeyLibraryReadmeMaxLines is the longest README the librarian may leave.
	KeyLibraryReadmeMaxLines = "library.readmeMaxLines"
	// DefaultLibraryReadmeMaxLines is the README limit until the owner sets one.
	DefaultLibraryReadmeMaxLines = 300
)

func (v *Values) loadLibrary(raw map[string]any) error {
	section, ok := raw["library"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("library settings must be an object")
	}
	if value, ok := values["enabled"]; ok {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := v.setLibrary(KeyLibraryEnabled, string(data)); err != nil {
			return err
		}
	}
	if value, ok := values["profile"]; ok {
		if err := v.setLibrary(KeyLibraryProfile, fmt.Sprint(value)); err != nil {
			return err
		}
	}
	if value, ok := values["smellProfile"]; ok {
		if err := v.setLibrary(KeyLibrarySmellProfile, fmt.Sprint(value)); err != nil {
			return err
		}
	}
	if value, ok := values["readmeMaxLines"]; ok {
		return v.setLibrary(KeyLibraryReadmeMaxLines, numberText(value))
	}
	return nil
}

func (v *Values) setLibrary(key, value string) error {
	switch key {
	case KeyLibraryProfile:
		v.LibraryProfile = strings.TrimSpace(value)
	case KeyLibrarySmellProfile:
		v.LibrarySmellProfile = strings.TrimSpace(value)
	case KeyLibraryReadmeMaxLines:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 {
			return fmt.Errorf("library.readmeMaxLines must be a positive number of lines")
		}
		v.LibraryReadmeMaxLines = n
	default:
		var repos []string
		if err := json.Unmarshal([]byte(value), &repos); err != nil {
			return fmt.Errorf("library.enabled must be a JSON list of absolute repository paths")
		}
		for i, repo := range repos {
			if !filepath.IsAbs(repo) || strings.ContainsRune(repo, 0) {
				return fmt.Errorf("library.enabled requires absolute repository paths")
			}
			repos[i] = filepath.Clean(repo)
		}
		v.LibraryEnabled = repos
	}
	v.fromFile[key] = true
	return nil
}

func (v Values) saveLibrary(raw map[string]any) {
	values := nested(raw, "library")
	values["enabled"], values["profile"], values["smellProfile"], values["readmeMaxLines"] = v.LibraryEnabled, v.LibraryProfile, v.LibrarySmellProfile, v.LibraryReadmeMaxLines
	raw["library"] = values
}

// LibraryOn reports whether the librarian runs for a main checkout path.
func (v Values) LibraryOn(main string) bool {
	return slices.Contains(v.LibraryEnabled, filepath.Clean(main))
}
