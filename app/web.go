// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:assets
var assetsFS embed.FS

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

func runWebServer() {
	mux := http.NewServeMux()

	handleUnifiedSearch := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		q := r.URL.Query()

		if entStr := q.Get("entity_id"); entStr != "" {
			if id, _ := strconv.Atoi(entStr); id > 0 {
				article, err := db.ArticleGetByEntity(id)
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
				article, err := db.ArticleGetByArticle(id)
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
				article, err := db.ArticleGet(id)
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

		radStr := q.Get("radius")
		rad, _ := strconv.ParseFloat(radStr, 64)

		yr, _ := strconv.Atoi(q.Get("year"))
		m, _ := strconv.Atoi(q.Get("month"))
		d, _ := strconv.Atoi(q.Get("day"))
		if m < 1 || m > 12 {
			m = 0
		}
		if d < 1 || d > 31 {
			d = 0
		}
		rng := parseRange(q.Get("range"))

		hasSpace := q.Has("latitude") && q.Has("longitude")
		hasTime := yr != 0 || m != 0 || d != 0
		hasText := query != ""

		if !hasText && !hasSpace && !hasTime {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]SearchResult{})
			return
		}

		if hasSpace && rad <= 0 {
			rad = 100.0
		}

		params := SearchParams{
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
			events, err := db.SearchEvents(params)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			results := make([]SearchResult, 0, len(events))
			for _, ev := range events {
				results = append(results, SearchResult{
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
					Type:      "E",
					Power:     100.0,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(results)
			return
		}

		var results []SearchResult
		var err error

		if mode == "semantic" {
			if !ai {
				http.Error(w, "Semantic search unavailable: AI model is not loaded", http.StatusBadRequest)
				return
			}
			results, err = db.SearchVectors(query, limit, params)
		} else if mode == "lexical" {
			results, err = db.SearchLexical(query, limit, params)
		} else if mode == "entity" {
			results, err = db.SearchEntityLabels(query, limit, params)
		} else {
			results, err = db.SearchHybrid(query, limit, params)
		}

		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		if len(results) > limit {
			results = results[:limit]
		}

		if results == nil {
			results = []SearchResult{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}

	mux.HandleFunc("/api", handleUnifiedSearch)
	mux.HandleFunc("/api/search", handleUnifiedSearch)
	mux.HandleFunc("/api/article", handleUnifiedSearch)
	mux.HandleFunc("/api/map", handleMap)
	mux.HandleFunc("/tiles/", handleTiles)
	mux.HandleFunc("/mcp", handleMCP)
	mux.HandleFunc("/", serveEmbeddedAssets)

	addr := fmt.Sprintf("%s:%d", options.webHost, options.webPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Listen error: %v", err)
	}

	log.Printf("Server listening on %s", addr)
	server := &http.Server{Handler: mux}
	log.Fatal(server.Serve(listener))
}

func parseRange(val string) int {
	v := strings.ToLower(strings.TrimSpace(val))
	switch v {
	case "lt", "lte", "<", "<=", "-1":
		return -1
	case "gt", "gte", ">", ">=", "+1", "1":
		return 1
	default:
		return 0
	}
}
