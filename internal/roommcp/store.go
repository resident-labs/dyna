package roommcp

import (
	"context"
	"database/sql"

	"dyna-agent/internal/database"
)

const schema = `
CREATE TABLE room_mcp_servers (
 room TEXT NOT NULL, name TEXT NOT NULL, url TEXT NOT NULL,
 credential_id TEXT NOT NULL, attached INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (room, name)
);
CREATE TABLE room_mcp_tools (
 room TEXT NOT NULL, server TEXT NOT NULL, tool TEXT NOT NULL,
 allowed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (room, server, tool),
 FOREIGN KEY (room, server) REFERENCES room_mcp_servers(room, name) ON DELETE CASCADE
);
CREATE TABLE room_mcp_grants (
 room TEXT NOT NULL, server TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('session', 'profile')),
 subject TEXT NOT NULL, granted_by TEXT NOT NULL,
 PRIMARY KEY (room, server, kind, subject),
 FOREIGN KEY (room, server) REFERENCES room_mcp_servers(room, name) ON DELETE CASCADE
);`

// Migrate creates the room MCP tables. Only the engine calls it, at startup.
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "room_mcp.schema", 1, func(conn *sql.Conn, old int) error {
		if old == 0 {
			_, err := conn.ExecContext(context.Background(), schema)
			return err
		}
		return nil
	})
}
