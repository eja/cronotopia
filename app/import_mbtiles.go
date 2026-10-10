// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"log"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func runMBTilesImport(db *DBHandler, srcDBPath string) error {
	conn := db.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer db.pool.Put(conn)

	log.Printf("Attaching MBTiles database: %s", srcDBPath)
	err := sqlitex.Execute(conn, "ATTACH DATABASE ? AS mbtiles", &sqlitex.ExecOptions{
		Args: []any{srcDBPath},
	})
	if err != nil {
		return fmt.Errorf("failed to attach database: %w", err)
	}
	defer func() {
		_ = sqlitex.ExecuteTransient(conn, "DETACH DATABASE mbtiles", nil)
	}()

	endTx, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer endTx(&err)

	log.Println("Clearing existing tiles and map metadata...")
	if err := sqlitex.Execute(conn, "DELETE FROM main.tiles", nil); err != nil {
		return fmt.Errorf("failed to clear tiles: %w", err)
	}
	_ = sqlitex.Execute(conn, `DELETE FROM main.settings WHERE key IN ('center', 'minzoom', 'maxzoom', 'name', 'description', 'attribution')`, nil)

	log.Println("Importing MBTiles metadata...")
	var hasMetadata bool
	_ = sqlitex.Execute(conn, "SELECT 1 FROM mbtiles.sqlite_master WHERE type IN ('table', 'view') AND name = 'metadata'", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			hasMetadata = true
			return nil
		},
	})

	if hasMetadata {
		_ = sqlitex.Execute(conn, `
			INSERT OR REPLACE INTO main.settings (key, value)
			SELECT name, value FROM mbtiles.metadata WHERE name IN ('center', 'minzoom', 'maxzoom', 'name', 'description', 'attribution')
		`, nil)
	}

	log.Println("Importing tiles...")
	err = sqlitex.Execute(conn, `
		INSERT OR REPLACE INTO main.tiles (zoom_level, tile_column, tile_row, tile_data)
		SELECT zoom_level, tile_column, tile_row, tile_data FROM mbtiles.tiles
	`, nil)
	if err != nil {
		return fmt.Errorf("import tiles error: %w", err)
	}

	return nil
}
