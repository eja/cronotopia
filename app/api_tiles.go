// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

//go:embed all:assets
var assetsFS embed.FS

type MapConfig struct {
	CenterLng float64 `json:"center_lng"`
	CenterLat float64 `json:"center_lat"`
	Zoom      float64 `json:"zoom"`
	MinZoom   int     `json:"min_zoom"`
	MaxZoom   int     `json:"max_zoom"`
}

func handleMapConfig(w http.ResponseWriter, r *http.Request) {
	cfg := db.GetMapConfig()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(cfg)
}

func handleTiles(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	z, _ := strconv.Atoi(parts[2])
	x, _ := strconv.Atoi(parts[3])
	yStr := strings.TrimSuffix(parts[4], ".pbf")
	y, _ := strconv.Atoi(yStr)

	data, err := db.GetTileData(z, x, y)
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(data)
}

func (h *DBHandler) GetTileData(z, x, y int) ([]byte, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	yTms := (1 << z) - 1 - y
	var tileData []byte
	err := sqlitex.Execute(conn, "SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?", &sqlitex.ExecOptions{
		Args: []any{z, x, yTms},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			tileData = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, tileData)
			return nil
		},
	})
	return tileData, err
}

func (h *DBHandler) GetMapConfig() MapConfig {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return MapConfig{Zoom: 1, MinZoom: 0, MaxZoom: 14}
	}
	defer h.pool.Put(conn)

	config := MapConfig{Zoom: 1, MinZoom: 0, MaxZoom: 14}
	_ = sqlitex.Execute(conn, "SELECT MIN(zoom_level), MAX(zoom_level) FROM tiles", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			config.MinZoom = int(stmt.ColumnInt64(0))
			config.MaxZoom = int(stmt.ColumnInt64(1))
			return nil
		},
	})
	config.Zoom = float64(config.MinZoom)
	if config.Zoom == 0 {
		config.Zoom = 1
	}

	var centerStr string
	_ = sqlitex.Execute(conn, "SELECT value FROM settings WHERE key = 'center'", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			centerStr = stmt.ColumnText(0)
			return nil
		},
	})

	if centerStr != "" {
		parts := strings.Split(centerStr, ",")
		if len(parts) >= 2 {
			config.CenterLng, _ = strconv.ParseFloat(parts[0], 64)
			config.CenterLat, _ = strconv.ParseFloat(parts[1], 64)
		}
	}
	return config
}

func serveEmbeddedAssets(w http.ResponseWriter, r *http.Request) {
	urlPath := path.Clean(r.URL.Path)
	if urlPath == "/" || urlPath == "." {
		urlPath = "/index.html"
	}

	targetPath := path.Join("assets", strings.TrimPrefix(urlPath, "/"))

	var data []byte
	var serveGzip bool

	f, err := assetsFS.Open(targetPath)
	if err == nil {
		fi, statErr := f.Stat()
		if statErr != nil || fi.IsDir() {
			f.Close()
			http.NotFound(w, r)
			return
		}
		data, err = io.ReadAll(f)
		f.Close()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(targetPath, ".gz") {
			serveGzip = true
		}
	} else {
		gzPath := targetPath + ".gz"
		gzFile, gzErr := assetsFS.Open(gzPath)
		if gzErr != nil {
			http.NotFound(w, r)
			return
		}
		data, err = io.ReadAll(gzFile)
		gzFile.Close()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		serveGzip = true
	}

	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		serveGzip = true
	}

	origName := strings.TrimSuffix(urlPath, ".gz")
	ext := strings.ToLower(path.Ext(origName))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		switch ext {
		case ".css":
			contentType = "text/css; charset=utf-8"
		case ".js":
			contentType = "application/javascript; charset=utf-8"
		case ".json":
			contentType = "application/json"
		case ".svg":
			contentType = "image/svg+xml"
		case ".pbf":
			contentType = "application/x-protobuf"
		case ".html":
			contentType = "text/html; charset=utf-8"
		default:
			contentType = "application/octet-stream"
		}
	}

	w.Header().Set("Content-Type", contentType)
	if serveGzip {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
