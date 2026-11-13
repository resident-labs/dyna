package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord/folder"
	"github.com/resident-labs/dyna/engine/discord/reconcile"
)

// DiscordFolderStore binds every ownership operation to its authenticated room.
type DiscordFolderStore struct {
	Store *Store
	Room  contract.RoomID
}

func (repository DiscordFolderStore) Ownership(ctx context.Context, guild string) ([]reconcile.Owned, error) {
	rows, err := repository.Store.Read().QueryContext(ctx, `SELECT kind,name,id,channel,hash,content FROM discord_folder_ownership WHERE room=? AND guild=? ORDER BY kind,name,id`, repository.Room, guild)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owned []reconcile.Owned
	for rows.Next() {
		var row reconcile.Owned
		var content string
		if err := rows.Scan(&row.Kind, &row.Name, &row.ID, &row.Channel, &row.Hash, &content); err != nil {
			return nil, err
		}
		row.Content = []byte(content)
		owned = append(owned, row)
	}
	return owned, rows.Err()
}

func (repository DiscordFolderStore) Record(ctx context.Context, guild string, row reconcile.Owned) error {
	return repository.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_folder_ownership(room,guild,kind,name,id,channel,hash,content) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(room,guild,kind,id) DO UPDATE SET name=excluded.name,channel=excluded.channel,hash=excluded.hash,content=excluded.content`, repository.Room, guild, row.Kind, row.Name, row.ID, row.Channel, row.Hash, string(row.Content))
		return err
	})
}

func (repository DiscordFolderStore) Forget(ctx context.Context, guild string, kind folder.Kind, id string) error {
	return repository.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM discord_folder_ownership WHERE room=? AND guild=? AND kind=? AND id=?`, repository.Room, guild, kind, id)
		return err
	})
}
