package hooks

import "path/filepath"

// Scope names where a hook's settings live. A repository hook belongs to the
// repository that stores it. A global hook belongs to the machine, has no
// repository, and runs whether or not a checkout is open.
type Scope string

const (
	ScopeRepository Scope = "repository"
	ScopeGlobal     Scope = "global"
)

// globalKey is the document key and identity of global hooks. A repository key
// is a hash, so it is never this word.
const globalKey = "global"

// key is the storage key of a hook identity: a repository ID, or the global
// scope itself.
func (s Service) key(id string) string {
	if id == globalKey {
		return globalKey
	}
	return filepath.Base(s.dir(id))
}
