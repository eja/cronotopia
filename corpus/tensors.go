// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package corpus

import (
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

func HasModel(conn *sqlite.Conn, schema string) bool {
	if schema == "" {
		schema = "main"
	}
	var exists bool
	_ = sqlitex.Execute(conn, "SELECT 1 FROM pragma_table_list WHERE schema = ? AND name = 'tensors' LIMIT 1", &sqlitex.ExecOptions{
		Args: []any{schema},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			exists = true
			return nil
		},
	})
	return exists
}

func ClearModel(conn *sqlite.Conn) error {
	return sqlitex.Execute(conn, "DELETE FROM tensors", nil)
}

func SaveTensor(conn *sqlite.Conn, t ModelTensor) error {
	return sqlitex.Execute(conn,
		"INSERT INTO tensors (layer, name, dtype, dims, data) VALUES (?, ?, ?, ?, ?)",
		&sqlitex.ExecOptions{
			Args: []any{t.Layer, t.Name, t.DType, t.Dims, t.Data},
		},
	)
}

func GetTensorBlob(conn *sqlite.Conn, schema string, rowID int64, offset int64, dest []byte) error {
	if schema == "" {
		schema = "main"
	}
	blob, err := conn.OpenBlob(schema, "tensors", "data", rowID, false)
	if err != nil {
		return fmt.Errorf("failed to open tensor blob (row %d in %s): %w", rowID, schema, err)
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

func LoadTensorInfos(conn *sqlite.Conn, schema string) (map[string]GGUFTensorInfo, error) {
	if schema == "" {
		schema = "main"
	}
	tensors := make(map[string]GGUFTensorInfo)
	err := sqlitex.Execute(conn, fmt.Sprintf("SELECT rowid, name, dtype, dims FROM %s.tensors", schema), &sqlitex.ExecOptions{
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
