package profile

// IsEnabled reports whether a matching profile exists and is enabled.
func IsEnabled(profiles []Profile, name string) bool {
	for _, p := range profiles {
		if p.Name == name && !p.Disabled {
			return true
		}
	}
	return false
}

// EnabledProfiles returns the profiles available for new work, in registry order.
func (s *Store) EnabledProfiles() []Profile {
	out := make([]Profile, 0, len(s.Profiles))
	for _, p := range s.Profiles {
		if !p.Disabled {
			out = append(out, p)
		}
	}
	return out
}
