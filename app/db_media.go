// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) MediaGet(id int, offset, length int64) ([]byte, int64, string, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, 0, "", fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	var totalLen int64
	var mode string
	var found bool

	err := sqlitex.Execute(conn, "SELECT LENGTH(content), COALESCE(mode, '') FROM media WHERE id = ? LIMIT 1", &sqlitex.ExecOptions{
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
	err = sqlitex.Execute(conn, "SELECT SUBSTR(content, ?, ?) FROM media WHERE id = ? LIMIT 1", &sqlitex.ExecOptions{
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
