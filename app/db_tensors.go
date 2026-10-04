// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
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
		"INSERT OR REPLACE INTO tensors (layer, name, dtype, dims, data) VALUES (?, ?, ?, ?, ?)",
		&sqlitex.ExecOptions{
			Args: []any{t.Layer, t.Name, t.DType, t.Dims, t.Data},
		},
	)
}

func (h *DBHandler) AiGetTensorData(name string) ([]byte, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	var data []byte
	err := sqlitex.Execute(conn, "SELECT data FROM tensors WHERE name = ? LIMIT 1", &sqlitex.ExecOptions{
		Args: []any{name},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			data = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, data)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("tensor %s not found in database", name)
	}
	return data, nil
}

func (h *DBHandler) AiLoadTensorInfos() (map[string]GGUFTensorInfo, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	tensors := make(map[string]GGUFTensorInfo)
	err := sqlitex.Execute(conn, "SELECT name, dtype, dims FROM tensors", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			name := stmt.ColumnText(0)
			dtype := uint32(stmt.ColumnInt(1))
			dimsStr := stmt.ColumnText(2)

			parts := strings.Split(dimsStr, ",")
			dims := make([]uint64, len(parts))
			for i, p := range parts {
				d, _ := strconv.ParseUint(p, 10, 64)
				dims[i] = d
			}

			tensors[name] = GGUFTensorInfo{
				Name:       name,
				Type:       dtype,
				Dimensions: dims,
			}
			return nil
		},
	})
	return tensors, err
}
