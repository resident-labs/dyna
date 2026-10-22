package settings

import (
	"fmt"
	"strings"
)

const (
	KeyBrowserPath         = "browser.path"
	KeyBrowserExtraFlags   = "browser.extraFlags"
	KeyBrowserUseFlagsFile = "browser.useFlagsFile"
	KeyBrowserProfileMode  = "browser.profileMode"
	KeyBrowserOwnerProfile = "browser.ownerProfile"
	KeyBrowserSaveBack     = "browser.saveBack"
)

func (v *Values) setBrowser(key, value string) error {
	switch key {
	case KeyBrowserPath:
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("browser.path must not contain NUL")
		}
		v.BrowserPath = value
	case KeyBrowserExtraFlags:
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("browser.extraFlags must not contain NUL")
		}
		v.BrowserExtraFlags = value
	case KeyBrowserProfileMode:
		if value != "shared" && value != "separate" && value != "copy-mine" {
			return fmt.Errorf("browser.profileMode must be shared, separate or copy-mine")
		}
		v.BrowserProfileMode = value
	case KeyBrowserOwnerProfile:
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
			return fmt.Errorf("browser.ownerProfile must be a profile directory name without path traversal")
		}
		v.BrowserOwnerProfile = value
	case KeyBrowserUseFlagsFile, KeyBrowserSaveBack:
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		if key == KeyBrowserUseFlagsFile {
			v.BrowserUseFlagsFile = b
		} else {
			v.BrowserSaveBack = b
		}
	}
	if v.fromFile == nil {
		v.fromFile = map[string]bool{}
	}
	v.fromFile[key] = true
	return nil
}

func (v *Values) loadBrowser(raw map[string]any) error {
	section, exists := raw["browser"]
	if !exists {
		return nil
	}
	browser, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("browser settings must be an object")
	}
	for _, key := range []string{KeyBrowserPath, KeyBrowserExtraFlags, KeyBrowserProfileMode, KeyBrowserOwnerProfile, KeyBrowserUseFlagsFile, KeyBrowserSaveBack} {
		value, exists := browser[strings.TrimPrefix(key, "browser.")]
		if !exists {
			continue
		}
		switch key {
		case KeyBrowserUseFlagsFile, KeyBrowserSaveBack:
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", key)
			}
		default:
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		}
		if err := v.setBrowser(key, fmt.Sprint(value)); err != nil {
			return err
		}
	}
	return nil
}

func (v Values) saveBrowser(raw map[string]any) {
	browser := nested(raw, "browser")
	browser["path"] = v.BrowserPath
	browser["extraFlags"] = v.BrowserExtraFlags
	browser["useFlagsFile"] = v.BrowserUseFlagsFile
	browser["profileMode"] = v.BrowserProfileMode
	browser["ownerProfile"] = v.BrowserOwnerProfile
	browser["saveBack"] = v.BrowserSaveBack
	raw["browser"] = browser
}
