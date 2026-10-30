package store

import (
	"context"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// RoomTree loads open sessions in a room for the pure tree rules.
func (s *Store) RoomTree(ctx context.Context, id contract.RoomID) (room.Tree, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,coalesce(parent,'') FROM sessions WHERE room=? AND closed_at IS NULL ORDER BY created_at,id", id)
	if err != nil {
		return room.Tree{}, err
	}
	defer rows.Close()
	var nodes []room.Node
	for rows.Next() {
		var node room.Node
		if err := rows.Scan(&node.ID, &node.Parent); err != nil {
			return room.Tree{}, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return room.Tree{}, err
	}
	return room.NewTree(nodes), nil
}
