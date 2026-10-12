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

	var totalTiles int64
	_ = sqlitex.Execute(conn, "SELECT COUNT(*) FROM mbtiles.tiles", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			totalTiles = stmt.ColumnInt64(0)
			return nil
		},
	})

	log.Printf("Importing %d tiles...", totalTiles)

	insertStmt, err := conn.Prepare("INSERT OR REPLACE INTO main.tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare insert statement error: %w", err)
	}
	defer insertStmt.Finalize()

	var count int64
	nextMilestone := 10

	err = sqlitex.Execute(conn, "SELECT zoom_level, tile_column, tile_row, tile_data FROM mbtiles.tiles", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			insertStmt.Reset()
			insertStmt.BindInt64(1, stmt.ColumnInt64(0))
			insertStmt.BindInt64(2, stmt.ColumnInt64(1))
			insertStmt.BindInt64(3, stmt.ColumnInt64(2))
			data := make([]byte, stmt.ColumnLen(3))
			stmt.ColumnBytes(3, data)
			insertStmt.BindBytes(4, data)

			if _, stepErr := insertStmt.Step(); stepErr != nil {
				return stepErr
			}

			count++
			if totalTiles > 0 {
				pct := int(count * 100 / totalTiles)
				if pct >= nextMilestone && nextMilestone <= 100 {
					log.Printf("Tile import progress: %d%% (%d / %d tiles)", nextMilestone, count, totalTiles)
					nextMilestone = (pct/10)*10 + 10
				}
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("import tiles error: %w", err)
	}

	return nil
}
