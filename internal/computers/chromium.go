package computers

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/shellquote"
)

//go:embed chromium.flags
var defaultChromiumFlags string

func computerBrowserProfile(id string) string { return filepath.Join(Dir(id), "profiles", "chromium") }

func chromiumFlagsFile(id string) string { return filepath.Join(Dir(id), "config", "chromium.flags") }
func chromiumWrapper(id string) string   { return filepath.Join(Dir(id), "bin", "dyna-chromium") }

func prepareChromium(id string) error {
	flags := chromiumFlagsFile(id)
	if err := os.MkdirAll(filepath.Dir(flags), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(flags, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(defaultChromiumFlags)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func chromiumBinary(name string) string {
	home, _ := os.UserHomeDir()
	if path := findChromiumBinary(chromiumBinaryCandidates(name, home)); path != "" {
		return path
	}
	return name
}

func chromiumBinaryCandidates(name, home string) []string {
	candidates := map[string][]string{
		"chromium": {
			"/usr/lib/chromium/chromium",
			"/snap/chromium/current/usr/lib/chromium-browser/chrome",
			"/opt/google/chrome/chrome",
			"/opt/brave.com/brave/brave",
		},
		"google-chrome":        {"/opt/google/chrome/chrome"},
		"google-chrome-stable": {"/opt/google/chrome/chrome"},
		"brave":                {"/opt/brave.com/brave/brave", "/usr/lib/brave/brave"},
	}
	paths := candidates[name]
	if name == "chromium" {
		patterns := []string{
			filepath.Join(home, ".agent-browser/browsers/chrome-*/chrome-linux64/chrome"),
			filepath.Join(home, ".agent-browser/browsers/chrome-*/chrome"),
			filepath.Join(home, ".cache/puppeteer/chrome/*/chrome-linux64/chrome"),
			filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux/chrome"),
			filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux64/chrome"),
		}
		if cache := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); cache != "" && cache != "0" {
			patterns = append([]string{filepath.Join(cache, "chromium-*/chrome-linux*/chrome")}, patterns...)
		}
		if cache := os.Getenv("PUPPETEER_CACHE_DIR"); cache != "" {
			patterns = append([]string{filepath.Join(cache, "chrome/*/chrome-linux64/chrome")}, patterns...)
		}
		paths = append(paths, patterns...)
	}
	return paths
}

func findChromiumBinary(patterns []string) string {
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		for _, candidate := range matches {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
				return candidate
			}
		}
	}
	return ""
}

// All browser shims ask the daemon for current settings at Chromium launch.
func chromiumScript(id string) string {
	exe, _ := os.Executable()
	return "#!/bin/sh\nDYNA_COMPUTER_PROCESS=" + shellquote.Quote(id) + "\nexport DYNA_COMPUTER_PROCESS\n" + computerPathScript() + "exec " + shellquote.Quote(exe) + " computers chromium " + shellquote.Quote(id) + " -- \"$@\"\n"
}

func computerPathScript() string {
	return "computer_path=\ncomputer_old_ifs=$IFS\nIFS=:\nfor computer_dir in $PATH; do\n case \"$computer_dir\" in " + shellquote.Quote(Root()) + "/*/bin ) ;; *) computer_path=${computer_path:+$computer_path:}$computer_dir ;; esac\ndone\nIFS=$computer_old_ifs\nPATH=$computer_path\nexport PATH\n"
}

// ChromiumExecutable finds the configured browser or the standard Chromium binary.
func ChromiumExecutable() (string, error) {
	v, err := settings.Load("")
	if err != nil {
		return "", err
	}
	return exec.LookPath(browserExecutable(v.BrowserPath))
}

func browserExecutable(configured string) string {
	if configured == "" {
		return chromiumBinary("chromium")
	}
	resolved := configured
	if !filepath.IsAbs(resolved) {
		if found, err := exec.LookPath(resolved); err == nil {
			resolved = found
		}
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	// Known distribution launchers source owner flags themselves. Bypass them
	// so the flags-file switch controls these supported Chromium installations.
	switch resolved {
	case "/usr/bin/chromium", "/usr/bin/chromium-browser":
		return chromiumBinary("chromium")
	case "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable":
		return chromiumBinary("google-chrome")
	case "/usr/bin/brave", "/usr/bin/brave-browser":
		return chromiumBinary("brave")
	}
	return configured
}
