package localci

import (
	"context"
	"fmt"

	"dyna-agent/internal/settings"
)

// LegacyRemoteNode maps the old remote setting to one enabled registered CI
// machine. The registered machine keeps its own name, SSH destination and slots.
// TODO: legacy/shim remove this reader when localci.remote is retired.
func (s Store) LegacyRemoteNode(ctx context.Context) (Node, error) {
	configured, err := settings.Load("")
	if err != nil {
		return Node{}, fmt.Errorf("read legacy localci.remote: %w", err)
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return Node{}, fmt.Errorf("list CI nodes for legacy localci.remote: %w", err)
	}
	return legacyRemoteNode(configured.LocalCIRemote, nodes)
}

func legacyRemoteNode(remote string, nodes []Node) (Node, error) {
	host, _, err := settings.ParseLocalCIRemote(remote)
	if err != nil {
		return Node{}, err
	}
	// Only the SSH host carries over. The registered runner owns its cache paths.
	var selected Node
	for _, node := range nodes {
		if !node.TakesCI() || node.SSH != host {
			continue
		}
		if selected.Name != "" {
			return Node{}, fmt.Errorf("localci.remote host %q matches multiple enabled registered CI nodes", host)
		}
		selected = node
	}
	if selected.Name == "" {
		return Node{}, fmt.Errorf("localci.remote host %q has no enabled registered CI node", host)
	}
	return selected, nil
}
