package engine

import (
	"context"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/library"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// libraryStore uses the socket caller's authority, never the engine environment.
func libraryStore(ctx context.Context) (library.Store, error) {
	caller, local := websvc.CallerFromContext(ctx)
	actor, _, err := mutationActor(caller, local, authority.Of(ctx), callspec.ClaimOf(ctx), enforceCallers, log.Printf, "changing the library")
	return library.Store{DataDir: store.DataDir(), Actor: actor}, err
}

func (s services) addLibraryEntry(ctx context.Context, repo string, entry library.Entry) (library.Entry, error) {
	st, err := libraryStore(ctx)
	if err != nil {
		return library.Entry{}, err
	}
	return st.Add(repo, entry)
}

func (s services) updateLibraryEntry(ctx context.Context, repo, id string, patch library.Patch) (library.Entry, error) {
	st, err := libraryStore(ctx)
	if err != nil {
		return library.Entry{}, err
	}
	return st.Update(repo, id, patch.Apply)
}

func (s services) linkLibraryEntry(ctx context.Context, repo, id string, docs, tests []string) (library.Entry, error) {
	st, err := libraryStore(ctx)
	if err != nil {
		return library.Entry{}, err
	}
	return st.Update(repo, id, func(e *library.Entry) error {
		e.Docs, e.Tests = library.Union(e.Docs, docs), library.Union(e.Tests, tests)
		return nil
	})
}

func (s services) retireLibraryEntry(ctx context.Context, repo, id, successor string) (library.Entry, error) {
	st, err := libraryStore(ctx)
	if err != nil {
		return library.Entry{}, err
	}
	return st.Retire(repo, id, successor)
}
