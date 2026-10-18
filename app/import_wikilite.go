// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func runWikiliteImport(db *DBHandler, srcDBPath string) error {
	conn := db.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer db.pool.Put(conn)

	log.Printf("Attaching Wikilite database: %s", srcDBPath)
	err := sqlitex.Execute(conn, "ATTACH DATABASE ? AS wikilite", &sqlitex.ExecOptions{
		Args: []any{srcDBPath},
	})
	if err != nil {
		return fmt.Errorf("failed to attach database: %w", err)
	}
	defer func() {
		_ = sqlitex.ExecuteTransient(conn, "DETACH DATABASE wikilite", nil)
	}()

	var ggufBlob []byte
	_ = sqlitex.Execute(conn, "SELECT value FROM wikilite.setup WHERE key = 'gguf' LIMIT 1", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			ggufBlob = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, ggufBlob)
			return nil
		},
	})

	endTx, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer func() {
		if endTx != nil {
			endTx(&err)
		}
	}()

	log.Println("Importing configuration...")
	configKeys := []string{"model", "annSize", "modelPrefixSearch", "modelPrefixSave"}
	for _, key := range configKeys {
		_ = sqlitex.Execute(conn, `
			INSERT OR REPLACE INTO main.settings (key, value)
			SELECT key, value FROM wikilite.setup WHERE key = ?
		`, &sqlitex.ExecOptions{Args: []any{key}})
	}

	log.Println("Importing entities...")
	err = sqlitex.Execute(conn, `
		INSERT INTO main.entities (id, article_id, title)
		SELECT 
			CAST(SUBSTR(entity, 2) AS INTEGER),
			id,
			title
		FROM wikilite.articles
		WHERE entity LIKE 'Q%'
		ON CONFLICT(id) DO UPDATE SET
			article_id = excluded.article_id,
			title = excluded.title
	`, nil)
	if err != nil {
		return fmt.Errorf("import entities error: %w", err)
	}

	log.Println("Checking sections table schema...")
	var hasPow bool
	_ = sqlitex.Execute(conn, "PRAGMA wikilite.table_info(sections)", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			colName := stmt.ColumnText(1)
			if colName == "pow" {
				hasPow = true
			}
			return nil
		},
	})

	powCol := "s.pow"
	if !hasPow {
		powCol = "0"
	}

	log.Println("Importing sections...")
	sectionsSQL := fmt.Sprintf(`
		INSERT OR REPLACE INTO main.sections (id, entity_id, title, content, pow)
		SELECT 
			s.id,
			CAST(SUBSTR(a.entity, 2) AS INTEGER),
			s.title,
			s.content,
			%s
		FROM wikilite.sections s
		JOIN wikilite.articles a ON s.article_id = a.id
		WHERE a.entity LIKE 'Q%%'
	`, powCol)

	err = sqlitex.Execute(conn, sectionsSQL, nil)
	if err != nil {
		return fmt.Errorf("import sections error: %w", err)
	}

	log.Println("Importing vectors and ANN clusters...")
	tables := []string{
		"vectors",
		"vectors_ann_chunks",
		"vectors_ann_centroids",
		"vectors_ann_centroid_chunks",
	}

	for _, tbl := range tables {
		var tableExists bool
		_ = sqlitex.Execute(conn, "SELECT 1 FROM wikilite.sqlite_master WHERE type='table' AND name = ?", &sqlitex.ExecOptions{
			Args: []any{tbl},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				tableExists = true
				return nil
			},
		})

		if !tableExists {
			continue
		}

		q := fmt.Sprintf("INSERT OR REPLACE INTO main.%s SELECT * FROM wikilite.%s", tbl, tbl)
		if err := sqlitex.Execute(conn, q, nil); err != nil {
			return fmt.Errorf("import %s error: %w", tbl, err)
		}
	}

	var hasAnnIndex bool
	_ = sqlitex.Execute(conn, "SELECT 1 FROM wikilite.sqlite_master WHERE type='table' AND name = 'vectors_ann_index'", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			hasAnnIndex = true
			return nil
		},
	})

	if hasAnnIndex {
		err = sqlitex.Execute(conn, `
			INSERT OR REPLACE INTO main.vectors_ann_index (chunk_id, chunk_position, vectors_id)
			SELECT chunk_id, chunk_position, vectors_id FROM wikilite.vectors_ann_index
		`, nil)
		if err != nil {
			return fmt.Errorf("import vectors_ann_index error: %w", err)
		}
	}

	log.Println("Rebuilding full-text search indexes (FTS5)...")
	err = sqlitex.Execute(conn, "INSERT INTO main.section_search(section_search) VALUES('rebuild')", nil)
	if err != nil {
		return fmt.Errorf("FTS5 section_search rebuild error: %w", err)
	}
	err = sqlitex.Execute(conn, "INSERT INTO main.entity_label_search(entity_label_search) VALUES('rebuild')", nil)
	if err != nil {
		return fmt.Errorf("FTS5 entity_label_search rebuild error: %w", err)
	}

	endTx(&err)
	endTx = nil
	if err != nil {
		return err
	}

	if len(ggufBlob) > 0 {
		log.Printf("Importing embedded GGUF model (%d bytes)...", len(ggufBlob))
		if err := ImportGGUFFromReader(db, bytes.NewReader(ggufBlob)); err != nil {
			return fmt.Errorf("import embedded gguf error: %w", err)
		}
	}

	return nil
}
