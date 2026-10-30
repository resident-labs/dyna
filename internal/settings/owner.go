package settings

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// KeyOwnerName is the name front ends show for the owner. Empty shows "you".
	KeyOwnerName = "owner.name"
	// KeyOwnerAvatar is the owner's avatar emoji. Empty shows the name's initials.
	KeyOwnerAvatar = "owner.avatar"

	// MaxOwnerName is the longest owner name, in characters.
	MaxOwnerName = 32
	// maxOwnerAvatar bounds one emoji, including joined sequences such as a
	// family or a flag with its modifiers.
	maxOwnerAvatar = 16
)

func (v *Values) loadOwner(raw map[string]any) error {
	section, ok := raw["owner"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("owner settings must be an object")
	}
	for _, key := range []string{KeyOwnerName, KeyOwnerAvatar} {
		value, ok := values[strings.TrimPrefix(key, "owner.")]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", key)
		}
		if err := v.setOwner(key, text); err != nil {
			return err
		}
	}
	return nil
}

func (v *Values) setOwner(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case KeyOwnerName:
		if utf8.RuneCountInString(value) > MaxOwnerName || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s must be at most %d characters on one line", key, MaxOwnerName)
		}
		v.OwnerName = value
	case KeyOwnerAvatar:
		// An emoji has no ASCII letters or spaces; this keeps a name or a
		// sentence out of a 32 px circle.
		if utf8.RuneCountInString(value) > maxOwnerAvatar || strings.IndexFunc(value, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r) || r < utf8.RuneSelf && unicode.IsLetter(r)
		}) >= 0 {
			return fmt.Errorf("%s must be one emoji, or empty", key)
		}
		v.OwnerAvatar = value
	}
	v.fromFile[key] = true
	return nil
}

func (v Values) saveOwner(raw map[string]any) {
	if v.OwnerName == "" && v.OwnerAvatar == "" {
		delete(raw, "owner")
		return
	}
	values := nested(raw, "owner")
	values["name"], values["avatar"] = v.OwnerName, v.OwnerAvatar
	raw["owner"] = values
}
