package folder

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/engine/discord"
)

// ReadExportSnapshot checks the destination before network reads and avoids
// interpreting configuration underneath existing unmanaged markers.
func ReadExportSnapshot(ctx context.Context, client *discord.Client, server, path string, force bool) (Snapshot, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return Snapshot{}, err
	}
	unmanaged, _, err := scanExportTree(root, force)
	if err != nil {
		return Snapshot{}, err
	}
	entries := make([]Entry, 0, len(unmanaged))
	for _, directory := range unmanaged {
		relative, ok := strings.CutPrefix(filepath.ToSlash(directory), "channels/")
		if !ok {
			return Snapshot{}, fmt.Errorf("UNMANAGED.md must mark a category or channel inside channels: %s", directory)
		}
		entries = append(entries, Entry{Path: relative, Unmanaged: "unmanaged"})
	}
	return ReadManagedSnapshot(ctx, client, server, entries)
}
