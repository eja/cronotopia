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

	return sqlitex.Execute(conn, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", &sqlitex.ExecOptions{
		Args: []any{key, value},
	})
}

func (h *DBHandler) SettingGet(key string) (string, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return "", fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	var val string
	var found bool
	err := sqlitex.Execute(conn, "SELECT value FROM settings WHERE key = ? LIMIT 1", &sqlitex.ExecOptions{
		Args: []any{key},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			val = stmt.ColumnText(0)
			found = true
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("key %q not found", key)
	}
	return val, nil
}
