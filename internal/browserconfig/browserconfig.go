// Package browserconfig discovers Chromium profile names and environment limits.
// It never reads cookies, passwords or other login state.
package browserconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Profile struct {
	Directory string
	Name      string
}

// BasicPasswordStore reports environments that have no usable owner keyring.
func BasicPasswordStore() bool {
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	container, _ := os.ReadFile("/run/systemd/container")
	return basicPasswordStore(string(kernel), os.Getenv("WSL_INTEROP"), os.Getenv("WSL_DISTRO_NAME"), string(container), os.Getenv("container"))
}

func basicPasswordStore(kernel, interop, distro, containerFile, containerEnv string) bool {
	return strings.Contains(strings.ToLower(kernel), "microsoft") ||
		strings.Contains(strings.ToLower(kernel), "wsl") ||
		interop != "" || distro != "" ||
		strings.TrimSpace(containerFile) == "systemd-nspawn" ||
		strings.TrimSpace(containerFile) == "nspawn" ||
		containerEnv == "systemd-nspawn" || containerEnv == "nspawn"
}

func CopyMineAvailable() bool { return !BasicPasswordStore() }

func OwnerRoot() string {
	root := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(root) {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "chromium")
}

// Profiles lists only direct, real directories with a Preferences file.
// Missing or malformed Local State falls back to directory names.
func Profiles() []Profile { return profiles(OwnerRoot()) }

func profiles(root string) []Profile {
	var state struct {
		Profile struct {
			InfoCache map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	data, _ := os.ReadFile(filepath.Join(root, "Local State"))
	_ = json.Unmarshal(data, &state)
	entries, _ := os.ReadDir(root)
	result := []Profile{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, entry.Name(), "Preferences"))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		name := state.Profile.InfoCache[entry.Name()].Name
		if name == "" {
			name = entry.Name()
		}
		result = append(result, Profile{Directory: entry.Name(), Name: name})
	}
	return result
}
