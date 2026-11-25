// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package media

import (
	"fmt"
	"log"
	"time"

	"cronotopia/vfs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Item struct {
	ID       int    `json:"id"`
	Author   string `json:"author,omitempty"`
	Title    string `json:"title,omitempty"`
	Language string `json:"language,omitempty"`
	Length   int64  `json:"length"`
	Mode     string `json:"mode,omitempty"`
}

func InitSchema(conn *sqlite.Conn) error {
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

func HasTable(conn *sqlite.Conn, schema string) bool {
	if schema == "" {
		schema = "main"
	}
	var exists bool
	_ = sqlitex.Execute(conn, "SELECT 1 FROM pragma_table_list WHERE schema = ? AND name = 'media' LIMIT 1", &sqlitex.ExecOptions{
		Args: []any{schema},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			exists = true
			return nil
		},
	})
	return exists
}

func tableQualifier(schema string) string {
	if schema == "" || schema == "main" {
		return "media"
	}
	return schema + ".media"
}

func Get(conn *sqlite.Conn, schema string, id int, offset, length int64) ([]byte, int64, string, error) {
	tbl := tableQualifier(schema)
	var totalLen int64
	var mode string
	var found bool

	err := sqlitex.Execute(conn, fmt.Sprintf("SELECT LENGTH(content), COALESCE(mode, '') FROM %s WHERE id = ? LIMIT 1", tbl), &sqlitex.ExecOptions{
		Args: []any{id},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			totalLen = stmt.ColumnInt64(0)
			mode = stmt.ColumnText(1)
			found = true
			return nil
		},
	})
	if err != nil {
		return nil, 0, "", err
	}
	if !found {
		return nil, 0, "", fmt.Errorf("media not found")
	}

	if offset < 0 {
		offset = 0
	}
	if offset >= totalLen {
		return []byte{}, totalLen, mode, nil
	}
	if length <= 0 || offset+length > totalLen {
		length = totalLen - offset
	}

	var data []byte
	err = sqlitex.Execute(conn, fmt.Sprintf("SELECT SUBSTR(content, ?, ?) FROM %s WHERE id = ? LIMIT 1", tbl), &sqlitex.ExecOptions{
		Args: []any{offset + 1, length, id},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			data = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, data)
			return nil
		},
	})
	if err != nil {
		return nil, 0, "", err
	}

	return data, totalLen, mode, nil
}

func GetByEntity(conn *sqlite.Conn, schema string, entityID int) ([]Item, error) {
	tbl := tableQualifier(schema)
	var items []Item
	err := sqlitex.Execute(conn, fmt.Sprintf(`
		SELECT id, COALESCE(author, ''), COALESCE(title, ''), COALESCE(language, ''), LENGTH(content), COALESCE(mode, '')
		FROM %s
		WHERE entity_id = ? OR entity_author_id = ?
		ORDER BY id ASC
	`, tbl), &sqlitex.ExecOptions{
		Args: []any{entityID, entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			items = append(items, Item{
				ID:       int(stmt.ColumnInt64(0)),
				Author:   stmt.ColumnText(1),
				Title:    stmt.ColumnText(2),
				Language: stmt.ColumnText(3),
				Length:   stmt.ColumnInt64(4),
				Mode:     stmt.ColumnText(5),
			})
			return nil
		},
	})
	return items, err
}

func Import(targetConn *sqlite.Conn, srcPath string) error {
	if err := InitSchema(targetConn); err != nil {
		return err
	}

	target, err := vfs.PrepareTarget(9999, srcPath)
	if err != nil {
		return fmt.Errorf("failed to prepare media source %s: %w", srcPath, err)
	}
	defer target.Close()

	srcConn, err := sqlite.OpenConn(target.URI, sqlite.OpenReadOnly|sqlite.OpenURI)
	if err != nil {
		return fmt.Errorf("failed to open media source %s: %w", srcPath, err)
	}
	defer srcConn.Close()

	if !HasTable(srcConn, "main") {
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
