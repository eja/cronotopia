// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package atlas

import (
	"fmt"
	"strconv"
	"strings"

	"cronotopia/vfs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Config struct {
	CenterLng float64 `json:"center_lng"`
	CenterLat float64 `json:"center_lat"`
	Zoom      float64 `json:"zoom"`
	MinZoom   int     `json:"min_zoom"`
	MaxZoom   int     `json:"max_zoom"`
}

func InitMapSchema(conn *sqlite.Conn) error {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS tiles_data (
			tile_data_id INTEGER PRIMARY KEY,
			tile_data BLOB
		);`,
		`CREATE TABLE IF NOT EXISTS tiles_shallow (
			zoom_level INTEGER,
			tile_column INTEGER,
			tile_row INTEGER,
			tile_data_id INTEGER,
			PRIMARY KEY(zoom_level, tile_column, tile_row)
		) WITHOUT ROWID;`,
		`CREATE VIEW IF NOT EXISTS tiles AS
		SELECT s.zoom_level, s.tile_column, s.tile_row, d.tile_data
		FROM tiles_shallow s
		JOIN tiles_data d ON s.tile_data_id = d.tile_data_id;`,
	}
	for _, s := range sqls {
		if err := sqlitex.ExecuteTransient(conn, s, nil); err != nil {
			return fmt.Errorf("error creating map schema: %w", err)
		}
	}
	return nil
}

func InitElevationSchema(conn *sqlite.Conn) error {
	sql := `CREATE TABLE IF NOT EXISTS elevations (
		zoom_level INTEGER,
		tile_column INTEGER,
		tile_row INTEGER,
		tile_data BLOB,
		PRIMARY KEY(zoom_level, tile_column, tile_row)
	) WITHOUT ROWID;`
	if err := sqlitex.ExecuteTransient(conn, sql, nil); err != nil {
		return fmt.Errorf("error creating elevation schema: %w", err)
	}
	return nil
}

func GetTileData(conn *sqlite.Conn, schema string, z, x, y int) ([]byte, error) {
	if z < 0 || z > 30 {
		return nil, fmt.Errorf("zoom level out of bounds: %d", z)
	}
	table := "tiles"
	if schema != "" && schema != "main" {
		table = schema + ".tiles"
	}
	yTms := (1 << z) - 1 - y
	var tileData []byte
	err := sqlitex.Execute(conn, fmt.Sprintf("SELECT tile_data FROM %s WHERE zoom_level=? AND tile_column=? AND tile_row=?", table), &sqlitex.ExecOptions{
		Args: []any{z, x, yTms},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			tileData = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, tileData)
			return nil
		},
	})
	return tileData, err
}

func GetElevationTileData(conn *sqlite.Conn, schema string, z, x, y int) ([]byte, error) {
	if z < 0 || z > 30 {
		return nil, fmt.Errorf("zoom level out of bounds: %d", z)
	}
	table := "elevations"
	if schema != "" && schema != "main" {
		table = schema + ".elevations"
	}
	yTms := (1 << z) - 1 - y
	var tileData []byte
	err := sqlitex.Execute(conn, fmt.Sprintf("SELECT tile_data FROM %s WHERE zoom_level=? AND tile_column=? AND tile_row=?", table), &sqlitex.ExecOptions{
		Args: []any{z, x, yTms},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			tileData = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, tileData)
			return nil
		},
	})
	return tileData, err
}

func GetMapConfig(conn *sqlite.Conn) Config {
	cfg := Config{Zoom: 1, MinZoom: 0, MaxZoom: 14}

	if val := vfs.GetSetting(conn, "map_minzoom"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.MinZoom = n
		}
	}
	if val := vfs.GetSetting(conn, "map_maxzoom"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.MaxZoom = n
		}
	}
	cfg.Zoom = float64(cfg.MinZoom)
	if cfg.Zoom == 0 {
		cfg.Zoom = 1
	}

	if centerStr := vfs.GetSetting(conn, "map_center"); centerStr != "" {
		parts := strings.Split(centerStr, ",")
		if len(parts) >= 2 {
			cfg.CenterLng, _ = strconv.ParseFloat(parts[0], 64)
			cfg.CenterLat, _ = strconv.ParseFloat(parts[1], 64)
		}
	}
	return cfg
}
