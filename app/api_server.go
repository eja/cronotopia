// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func runAPIServer() {
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

		if q.Get("map") == "1" || q.Get("config") == "1" {
			handleMapConfig(w, r)
			return
		}

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
		rng := parseRange(q.Get("range"))

		hasSpace := lat != 0 || lon != 0
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
			results, err = db.SearchVectors(query, limit, params)
		} else if mode == "lexical" {
			results, err = db.SearchLexical(query, limit, params)
		} else if mode == "entity" {
			results, err = db.SearchEntityLabels(query, limit, params)
		} else {
			seen := make(map[int]bool)

			entityQuota := limit / 4
			if entityQuota < 1 {
				entityQuota = 1
			} else if entityQuota > 3 {
				entityQuota = 3
			}

			entResults, _ := db.SearchEntityLabels(query, entityQuota, params)
			for _, r := range entResults {
				if !seen[r.EntityID] {
					seen[r.EntityID] = true
					results = append(results, r)
				}
			}

			neededLexical := limit - len(results)
			if ai && neededLexical > 3 {
				neededLexical -= 2
			}
			if neededLexical > 0 {
				lexResults, _ := db.SearchLexical(query, neededLexical, params)
				for _, r := range lexResults {
					if !seen[r.EntityID] {
						seen[r.EntityID] = true
						results = append(results, r)
					}
				}
			}

			if len(results) < limit && ai {
				semResults, _ := db.SearchVectors(query, limit-len(results), params)
				for _, r := range semResults {
					if !seen[r.EntityID] {
						seen[r.EntityID] = true
						results = append(results, r)
					}
				}
			}
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
	mux.HandleFunc("/api/map", handleMapConfig)
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
