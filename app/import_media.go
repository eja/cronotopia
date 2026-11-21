// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func initMediaSchema(conn *sqlite.Conn) error {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_id INTEGER,
			entity_author_id INTEGER,
			language TEXT,
			mode TEXT,
			author TEXT,
			title TEXT,
			content BLOB
		);`,
		`CREATE INDEX IF NOT EXISTS idx_media_entity_id ON media(entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_media_author_id ON media(entity_author_id);`,
	}
	for _, s := range sqls {
		if err := sqlitex.ExecuteTransient(conn, s, nil); err != nil {
			return fmt.Errorf("error creating media schema: %w", err)
		}
	}
	return nil
}

func runMediaImport(targetDB *DBHandler, srcPath string) error {
	targetConn := targetDB.pool.Get(context.Background())
	if targetConn == nil {
		return fmt.Errorf("failed to get target db connection")
	}
	defer targetDB.pool.Put(targetConn)

	if err := initMediaSchema(targetConn); err != nil {
		return err
	}

	savedImport := options.mediaImport
	options.mediaImport = ""
	srcDB, err := NewDBHandler(srcPath)
	options.mediaImport = savedImport
	if err != nil {
		return fmt.Errorf("failed to open source database %s: %w", srcPath, err)
	}
	defer srcDB.Close()

	srcConn := srcDB.pool.Get(context.Background())
	if srcConn == nil {
		return fmt.Errorf("failed to get source db connection")
	}
	defer srcDB.pool.Put(srcConn)

	if !hasTable(srcConn, "media") {
		return fmt.Errorf("source database does not contain media table")
	}

	log.Printf("Starting media import from %s...", srcPath)
	start := time.Now()

	stmtInsert := targetConn.Prep(`
		INSERT INTO media (entity_id, entity_author_id, language, mode, author, title, content)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)

	endTx, err := sqlitex.ImmediateTransaction(targetConn)
	if err != nil {
		return err
	}
	defer func() {
		if endTx != nil {
			endTx(&err)
		}
	}()

	var count int
	const batchSize = 500

	err = sqlitex.Execute(srcConn, `
		SELECT entity_id, entity_author_id, language, mode, author, title, content
		FROM media
	`, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			stmtInsert.BindInt64(1, stmt.ColumnInt64(0))
			stmtInsert.BindInt64(2, stmt.ColumnInt64(1))
			stmtInsert.BindText(3, stmt.ColumnText(2))
			stmtInsert.BindText(4, stmt.ColumnText(3))
			stmtInsert.BindText(5, stmt.ColumnText(4))
			stmtInsert.BindText(6, stmt.ColumnText(5))

			blobLen := stmt.ColumnLen(6)
			blob := make([]byte, blobLen)
			stmt.ColumnBytes(6, blob)
			stmtInsert.BindBytes(7, blob)

			if _, stepErr := stmtInsert.Step(); stepErr != nil {
				return fmt.Errorf("insert media step error: %w", stepErr)
			}
			stmtInsert.Reset()

			count++
			if count%batchSize == 0 {
				var commitErr error
				endTx(&commitErr)
				if commitErr != nil {
					return fmt.Errorf("batch commit error: %w", commitErr)
				}
				endTx, err = sqlitex.ImmediateTransaction(targetConn)
				if err != nil {
					return fmt.Errorf("transaction reopen error: %w", err)
				}
				log.Printf("Imported %d media entries...", count)
			}
			return nil
		},
	})
	if err != nil {
		return err
	}

	var finalCommitErr error
	endTx(&finalCommitErr)
	endTx = nil
	if finalCommitErr != nil {
		return fmt.Errorf("final commit error: %w", finalCommitErr)
	}

	log.Printf("Media import completed: %d entries in %s", count, time.Since(start).Round(time.Millisecond))
	return nil
}
