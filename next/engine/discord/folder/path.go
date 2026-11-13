package folder

import (
	"fmt"
	"os"
	"path/filepath"
)

// ServerPath never silently abandons a private source tree when mappings change.
// A previously nested server stays nested even when it becomes the only mapping.
func ServerPath(roomPath, server string, multiple bool) (string, error) {
	root := filepath.Join(roomPath, "discord")
	nested := filepath.Join(root, server)
	_, nestedErr := os.Stat(filepath.Join(nested, "server.yaml"))
	if nestedErr != nil && !os.IsNotExist(nestedErr) {
		return "", nestedErr
	}
	_, rootErr := os.Stat(filepath.Join(root, "server.yaml"))
	if rootErr != nil && !os.IsNotExist(rootErr) {
		return "", rootErr
	}
	if rootErr == nil && (multiple || nestedErr == nil) {
		return "", fmt.Errorf("the existing server source is still at %s; nothing was changed. Before using several mappings, move that entire folder, including policy.yaml, messages and UNMANAGED.md, to a temporary sibling, then into discord/ORIGINAL_SERVER_ID/. Keep each server in its own ID folder", root)
	}
	if multiple || nestedErr == nil {
		return nested, nil
	}
	return root, nil
}
