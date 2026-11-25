// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"cronotopia/acta"
	"cronotopia/atlas"
	"cronotopia/corpus"
	"cronotopia/media"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Server struct {
	pool   *sqlitex.Pool
	engine *corpus.Engine
}

func NewServer(pool *sqlitex.Pool, engine *corpus.Engine) *Server {
	return &Server{
		pool:   pool,
		engine: engine,
	}
}

func (s *Server) tableSchema(conn *sqlite.Conn, table string) string {
	candidates := []string{"corpus_db", "map_db", "elevation_db", "media_db", "main"}
	for _, sc := range candidates {
		var exists bool
		_ = sqlitex.Execute(conn, "SELECT 1 FROM pragma_table_list WHERE schema = ? AND name = ? LIMIT 1", &sqlitex.ExecOptions{
			Args: []any{sc, table},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				exists = true
				return nil
			},
		})
		if exists {
			var hasRow bool
			_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT 1 FROM %s.%s LIMIT 1", sc, table), &sqlitex.ExecOptions{
				ResultFunc: func(stmt *sqlite.Stmt) error {
					hasRow = true
					return nil
				},
			})
			if hasRow {
				return sc
			}
		}
	}
	for _, sc := range candidates {
		var exists bool
		_ = sqlitex.Execute(conn, "SELECT 1 FROM pragma_table_list WHERE schema = ? AND name = ? LIMIT 1", &sqlitex.ExecOptions{
			Args: []any{sc, table},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				exists = true
				return nil
			},
		})
		if exists {
			return sc
		}
	}
	return "main"
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	handleUnifiedSearch := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		conn := s.pool.Get(r.Context())
		if conn == nil {
			http.Error(w, "Database busy", http.StatusServiceUnavailable)
			return
		}
		defer s.pool.Put(conn)

		q := r.URL.Query()

		if entStr := q.Get("entity_id"); entStr != "" {
			if id, _ := strconv.Atoi(entStr); id > 0 {
				article, err := corpus.GetArticleByEntity(conn, id)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(article)
				return
			}
		}

		if artStr := q.Get("article_id"); artStr != "" {
			if id, _ := strconv.Atoi(artStr); id > 0 {
				article, err := corpus.GetArticleByArticle(conn, id)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(article)
				return
			}
		}

		if idStr := q.Get("id"); idStr != "" {
			if id, _ := strconv.Atoi(idStr); id > 0 {
				article, err := corpus.GetArticle(conn, id)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(article)
				return
			}
		}

		query := strings.TrimSpace(q.Get("query"))
		mode := q.Get("mode")
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = 10
		}

		lat, _ := strconv.ParseFloat(q.Get("latitude"), 64)
		lon, _ := strconv.ParseFloat(q.Get("longitude"), 64)
		rad, _ := strconv.ParseFloat(q.Get("radius"), 64)

		yr, _ := strconv.Atoi(q.Get("year"))
		m, _ := strconv.Atoi(q.Get("month"))
		d, _ := strconv.Atoi(q.Get("day"))
		if m < 1 || m > 12 {
			m = 0
		}
		if d < 1 || d > 31 {
			d = 0
		}

		rng := 0
		switch strings.ToLower(strings.TrimSpace(q.Get("range"))) {
		case "lt", "lte", "<", "<=", "-1":
			rng = -1
		case "gt", "gte", ">", ">=", "+1", "1":
			rng = 1
		}

		hasSpace := q.Has("latitude") && q.Has("longitude")
		hasTime := yr != 0 || m != 0 || d != 0
		hasText := query != ""

		if !hasText && !hasSpace && !hasTime {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]acta.SearchResult{})
			return
		}

		if hasSpace && rad <= 0 {
			rad = 100.0
		}

		params := acta.SearchParams{
			QueryText: query,
			Lat:       lat,
			Lon:       lon,
			RadiusKm:  rad,
			HasSpace:  hasSpace,
			HasTime:   hasTime,
			Year:      yr,
			Month:     m,
			Day:       d,
			Range:     rng,
			Limit:     limit,
		}

		if !hasText && (hasSpace || hasTime) {
			events, err := acta.SearchEvents(conn, params)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			results := make([]acta.SearchResult, 0, len(events))
			for _, ev := range events {
				results = append(results, acta.SearchResult{
					EntityID:  ev.ID,
					ArticleID: ev.ArticleID,
					Title:     ev.Label,
					Text:      ev.Description,
					Snippet:   ev.Description,
					Lat:       ev.Lat,
					Lon:       ev.Lon,
					Year:      ev.Year,
					Code:      ev.Code,
					DateBegin: ev.DateBegin,
					DateEnd:   ev.DateEnd,
					Times:     ev.Times,
					Places:    ev.Places,
					Media:     ev.Media,
					Type:      "E",
					Power:     100.0,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(results)
			return
		}

		var results []acta.SearchResult
		var err error

		if mode == "semantic" {
			if s.engine == nil {
				http.Error(w, "Semantic search unavailable: AI model is not loaded", http.StatusBadRequest)
				return
			}
			results, err = corpus.SearchVectors(conn, s.engine, query, limit, params)
		} else if mode == "lexical" {
			results, err = corpus.SearchLexical(conn, query, limit, params)
		} else if mode == "entity" {
			results, err = acta.SearchEntityLabels(conn, query, limit, params)
		} else {
			results, err = corpus.SearchHybrid(conn, s.engine, query, limit, params)
		}

		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(results) > limit {
			results = results[:limit]
		}
		if results == nil {
			results = []acta.SearchResult{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}

	mux.HandleFunc("/api", handleUnifiedSearch)
	mux.HandleFunc("/api/search", handleUnifiedSearch)
	mux.HandleFunc("/api/article", handleUnifiedSearch)
	mux.HandleFunc("/api/media", s.handleMedia)
	mux.HandleFunc("/api/map", s.handleMap)
	mux.HandleFunc("/tiles/", s.handleTiles)
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/", ServeAssets)

	return mux
}

func (s *Server) handleTiles(w http.ResponseWriter, r *http.Request) {
	cleanPath := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(cleanPath, "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}

	isElevation := len(parts) >= 4 && parts[1] == "elevations"

	z, errZ := strconv.Atoi(parts[len(parts)-3])
	x, errX := strconv.Atoi(parts[len(parts)-2])
	yStr := parts[len(parts)-1]
	yStr = strings.TrimSuffix(yStr, ".pbf")
	yStr = strings.TrimSuffix(yStr, ".png")
	y, errY := strconv.Atoi(yStr)

	if errZ != nil || errX != nil || errY != nil || z < 0 || z > 30 {
		http.NotFound(w, r)
		return
	}

	conn := s.pool.Get(r.Context())
	if conn == nil {
		http.Error(w, "Database busy", http.StatusServiceUnavailable)
		return
	}
	defer s.pool.Put(conn)

	if isElevation {
		schema := s.tableSchema(conn, "elevations")
		data, err := atlas.GetElevationTileData(conn, schema, z, x, y)
		if err != nil || len(data) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write(data)
		return
	}

	schema := s.tableSchema(conn, "tiles")
	data, err := atlas.GetTileData(conn, schema, z, x, y)
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

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Accept-Ranges", "bytes")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("id"))
	if id <= 0 {
		http.Error(w, "Missing or invalid id", http.StatusBadRequest)
		return
	}

	var offset int64
	var length int64
	if offStr := q.Get("offset"); offStr != "" {
		offset, _ = strconv.ParseInt(offStr, 10, 64)
	}
	if lenStr := q.Get("length"); lenStr != "" {
		length, _ = strconv.ParseInt(lenStr, 10, 64)
	}

	rangeHeader := r.Header.Get("Range")
	isRange := false
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if len(parts) == 2 {
			if start, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				offset = start
				isRange = true
				if end, err := strconv.ParseInt(parts[1], 10, 64); err == nil && end >= start {
					length = end - start + 1
				}
			}
		}
	}

	conn := s.pool.Get(r.Context())
	if conn == nil {
		http.Error(w, "Database busy", http.StatusServiceUnavailable)
		return
	}
	defer s.pool.Put(conn)

	schema := s.tableSchema(conn, "media")
	data, totalLen, mode, err := media.Get(conn, schema, id, offset, length)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	contentType := "text/plain; charset=utf-8"
	switch mode {
	case "score":
		contentType = "application/vnd.recordare.musicxml+xml; charset=utf-8"
	case "book":
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)

	if isRange {
		endByte := offset + int64(len(data)) - 1
		if len(data) == 0 {
			endByte = offset
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, endByte, totalLen))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusPartialContent)
	} else if offset > 0 || (length > 0 && length < totalLen) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
		w.WriteHeader(http.StatusOK)
	}

	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	conn := s.pool.Get(r.Context())
	if conn == nil {
		http.Error(w, "Database busy", http.StatusServiceUnavailable)
		return
	}
	defer s.pool.Put(conn)

	q := r.URL.Query()
	settingsSchema := s.tableSchema(conn, "settings")
	mapSchema := s.tableSchema(conn, "tiles")

	if idStr := q.Get("id"); idStr != "" {
		latStr := q.Get("latitude")
		lonStr := q.Get("longitude")
		lat, _ := strconv.ParseFloat(latStr, 64)
		lon, _ := strconv.ParseFloat(lonStr, 64)

		rad, _ := strconv.ParseFloat(q.Get("radius"), 64)
		if rad <= 0 {
			rad = 1000.0
		}

		zoomStr := q.Get("zoom")
		zoom, err := strconv.Atoi(zoomStr)
		if zoomStr == "" || err != nil || zoom < 0 {
			zoom = atlas.GetMapConfig(conn, settingsSchema).MaxZoom
		}

		minLat, maxLat, minLon, maxLon := atlas.BoundingBox(lat, lon, rad/1000.0)
		feat, err := atlas.ExtractFeatureByID(conn, mapSchema, idStr, minLon, minLat, maxLon, maxLat, zoom)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if feat == nil {
			http.Error(w, "Feature not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(feat)
		return
	}

	hasCoords := q.Has("latitude") || q.Has("longitude")
	if hasCoords {
		latStr := q.Get("latitude")
		lonStr := q.Get("longitude")
		lat, _ := strconv.ParseFloat(latStr, 64)
		lon, _ := strconv.ParseFloat(lonStr, 64)

		rad, _ := strconv.ParseFloat(q.Get("radius"), 64)
		if rad <= 0 {
			rad = 1000.0
		}

		zoomStr := q.Get("zoom")
		zoom, err := strconv.Atoi(zoomStr)
		if zoomStr == "" || err != nil || zoom < 0 {
			zoom = atlas.GetMapConfig(conn, settingsSchema).MaxZoom
		}

		centroids, err := atlas.ExtractTileCentroids(conn, mapSchema, lat, lon, rad, zoom)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if centroids == nil {
			centroids = []atlas.TileCentroid{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(centroids)
		return
	}

	cfg := atlas.GetMapConfig(conn, settingsSchema)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cfg)
}

func (s *Server) Start(host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("Server listening on %s", addr)
	srv := &http.Server{Handler: s.Routes()}
	return srv.Serve(listener)
}
