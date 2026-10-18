// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type ModelTensor struct {
	Layer int    `json:"layer"`
	Name  string `json:"name"`
	DType int    `json:"dtype"`
	Dims  string `json:"dims"`
	Data  []byte `json:"-"`
}

func (h *DBHandler) AiHasModel() bool {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return false
	}
	defer h.pool.Put(conn)

	var found bool
	_ = sqlitex.Execute(conn, "SELECT 1 FROM tensors LIMIT 1", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			found = true
			return nil
		},
	})
	return found
}

func (h *DBHandler) AiClearModel() error {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	return sqlitex.Execute(conn, "DELETE FROM tensors", nil)
}

func (h *DBHandler) AiSaveTensor(conn *sqlite.Conn, t ModelTensor) error {
	return sqlitex.Execute(conn,
		"INSERT INTO tensors (layer, name, dtype, dims, data) VALUES (?, ?, ?, ?, ?)",
		&sqlitex.ExecOptions{
			Args: []any{t.Layer, t.Name, t.DType, t.Dims, t.Data},
		},
	)
}

func (h *DBHandler) AiGetTensorBlob(rowID int64, offset int64, dest []byte) error {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	blob, err := conn.OpenBlob("", "tensors", "data", rowID, false)
	if err != nil {
		return fmt.Errorf("failed to open tensor blob (row %d): %w", rowID, err)
	}
	defer blob.Close()

	if offset > 0 {
		if _, err := blob.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("failed to seek tensor blob (row %d, offset %d): %w", rowID, offset, err)
		}
	}

	if _, err := io.ReadFull(blob, dest); err != nil {
		return fmt.Errorf("failed to read tensor blob (row %d, len %d): %w", rowID, len(dest), err)
	}
	return nil
}

func (h *DBHandler) AiLoadTensorInfos() (map[string]GGUFTensorInfo, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	tensors := make(map[string]GGUFTensorInfo)
	err := sqlitex.Execute(conn, "SELECT rowid, name, dtype, dims FROM tensors", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			rowID := stmt.ColumnInt64(0)
			name := stmt.ColumnText(1)
			dtype := uint32(stmt.ColumnInt(2))
			dimsStr := stmt.ColumnText(3)

			parts := strings.Split(dimsStr, ",")
			dims := make([]uint64, len(parts))
			for i, p := range parts {
				d, _ := strconv.ParseUint(p, 10, 64)
				dims[i] = d
			}

			tensors[name] = GGUFTensorInfo{
				RowID:      rowID,
				Name:       name,
				Type:       dtype,
				Dimensions: dims,
			}
			return nil
		},
	})
	return tensors, err
}
