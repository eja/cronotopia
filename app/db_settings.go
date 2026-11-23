// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) SettingPut(key, value string) error {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	return sqlitex.Execute(conn, "INSERT OR REPLACE INTO main.settings (key, value) VALUES (?, ?)", &sqlitex.ExecOptions{
		Args: []any{key, value},
	})
}

func (h *DBHandler) SettingGet(key string) (string, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return "", fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	schemas := []string{"main"}
	for _, target := range h.attached {
		schemas = append(schemas, target.schema)
	}

	for _, schema := range schemas {
		if !hasSchemaTable(conn, schema, "settings") {
			continue
		}
		var val string
		var found bool
		query := fmt.Sprintf("SELECT value FROM %s.settings WHERE key = ? LIMIT 1", schema)
		err := sqlitex.Execute(conn, query, &sqlitex.ExecOptions{
			Args: []any{key},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				val = stmt.ColumnText(0)
				found = true
				return nil
			},
		})
		if err == nil && found {
			return val, nil
		}
	}

	return "", fmt.Errorf("key %q not found", key)
}
