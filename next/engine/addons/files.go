// Package addons keeps a scope's skills, Pi packages and MCP servers in files beside its instructions, and packs each
// scope into the one file a host mirrors and a box unpacks. Room deletion removes a room's add-ons with its folder.
package addons

import (
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

// ErrMissing says a scope has no such item.
var ErrMissing = errors.New("no such add-on")

// Files reads and writes every scope's add-ons.
type Files struct {
	Layout box.Layout
	mu     sync.RWMutex
}

func (f *Files) read(room contract.RoomID) ([]contract.Addon, bool, error) {
	data, err := os.ReadFile(f.Layout.Addons(room))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var list skills.List
	return list.Items, true, json.Unmarshal(data, &list)
}

// write replaces the list atomically, as instructions are saved.
func (f *Files) write(room contract.RoomID, items []contract.Addon) error {
	slices.SortFunc(items, func(a, b contract.Addon) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	data, err := json.MarshalIndent(skills.List{Items: items}, "", "  ")
	if err != nil {
		return err
	}
	path := f.Layout.Addons(room)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".addons-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func same(kind contract.AddonKind, name string) func(contract.Addon) bool {
	return func(item contract.Addon) bool { return item.Kind == kind && item.Name == name }
}

// List reads one scope.
func (f *Files) List(room contract.RoomID) ([]contract.Addon, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	items, _, err := f.read(room)
	for i := range items {
		items[i].Room = room
	}
	return items, err
}

// Has reports whether a scope holds an item.
func (f *Files) Has(room contract.RoomID, kind contract.AddonKind, name string) (bool, error) {
	items, err := f.List(room)
	return slices.ContainsFunc(items, same(kind, name)), err
}

func (f *Files) put(room contract.RoomID, item contract.Addon) error {
	items, _, err := f.read(room)
	if err != nil {
		return err
	}
	return f.write(room, append(slices.DeleteFunc(items, same(item.Kind, item.Name)), item))
}

// Put adds an item, or replaces the one of that kind and name.
func (f *Files) Put(room contract.RoomID, item contract.Addon) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.put(room, item)
}

// SaveSkill puts a skill's archive in the scope's folder, in place of any skill of that name.
func (f *Files) SaveSkill(room contract.RoomID, item contract.Addon, archive []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := f.Layout.Skills(room)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unpacked, err := os.MkdirTemp(dir, ".save-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(unpacked)
	if err := skills.Extract(archive, unpacked); err != nil {
		return err
	}
	target := filepath.Join(dir, item.Name)
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.Rename(unpacked, target); err != nil {
		return err
	}
	return f.put(room, item)
}

// Remove takes an item out of a scope.
func (f *Files) Remove(room contract.RoomID, kind contract.AddonKind, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	items, _, err := f.read(room)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(items, same(kind, name)) {
		return ErrMissing
	}
	if kind == contract.AddonSkill {
		if err := os.RemoveAll(filepath.Join(f.Layout.Skills(room), name)); err != nil {
			return err
		}
	}
	return f.write(room, slices.DeleteFunc(items, same(kind, name)))
}

// SetEnabled turns an item on or off.
func (f *Files) SetEnabled(room contract.RoomID, kind contract.AddonKind, name string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	items, _, err := f.read(room)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(items, same(kind, name))
	if i < 0 {
		return ErrMissing
	}
	items[i].Disabled = !enabled
	return f.write(room, items)
}

// Bundle packs what a box of this scope gets: the list of enabled items and their skill folders. It is nil for a scope
// that has never held anything, so such a scope mirrors nothing, and a scope emptied later mirrors an empty list.
func (f *Files) Bundle(room contract.RoomID) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	items, found, err := f.read(room)
	if err != nil || !found {
		return nil, err
	}
	archive := skills.NewArchive()
	enabled := slices.DeleteFunc(items, func(item contract.Addon) bool { return item.Disabled })
	list, err := json.Marshal(skills.List{Items: enabled})
	if err != nil {
		return nil, err
	}
	if err := archive.Add(skills.ListFile, 0o600, list); err != nil {
		return nil, err
	}
	for _, item := range enabled {
		if item.Kind == contract.AddonSkill {
			if err := archive.Folder(item.Name, filepath.Join(f.Layout.Skills(room), item.Name)); err != nil {
				return nil, err
			}
		}
	}
	return archive.Bytes()
}
