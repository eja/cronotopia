package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

//go:embed assets
var assetsFS embed.FS

func runServer(dbFile, host, port string) {
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		log.Fatalf("Database file %s not found.", dbFile)
	}

	log.Printf("Connecting to database %s...", dbFile)
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(20)
	defer db.Close()

	mapConfig := GetMapConfig(db)
	log.Printf("Map Config: Center [%.4f, %.4f], Zoom %.1f, Min: %d, Max: %d", mapConfig.CenterLng, mapConfig.CenterLat, mapConfig.Zoom, mapConfig.MinZoom, mapConfig.MaxZoom)

	assetsRoot, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatal("Failed to load embedded assets:", err)
	}

	fileServer := http.FileServer(http.FS(assetsRoot))
	http.Handle("/js/", fileServer)
	http.Handle("/css/", fileServer)
	http.Handle("/fonts/", fileServer)
	http.Handle("/sprites/", fileServer)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}

		tmpl, err := template.ParseFS(assetsFS, "assets/index.html")
		if err != nil {
			http.Error(w, "Could not load embedded template: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		err = tmpl.Execute(w, mapConfig)
		if err != nil {
			log.Printf("Template execution error: %v", err)
		}
	})

	http.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		handleAPI(w, r, db)
	})

	http.HandleFunc("/tiles/", func(w http.ResponseWriter, r *http.Request) {
		serveTiles(w, r, db)
	})

	addr := host
	if addr == "" {
		addr = "localhost"
	}
	addr = addr + ":" + port

	log.Printf("Server listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleAPI(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	query := r.URL.Query()

	params := SearchParams{
		Lang:      query.Get("language"),
		QueryText: query.Get("query"),
	}

	if params.Lang == "" {
		params.Lang = "en"
	}

	params.Day, _ = strconv.Atoi(query.Get("day"))
	params.Month, _ = strconv.Atoi(query.Get("month"))
	params.Year, _ = strconv.Atoi(query.Get("year"))
	params.Lat, _ = strconv.ParseFloat(query.Get("latitude"), 64)
	params.Lon, _ = strconv.ParseFloat(query.Get("longitude"), 64)
	params.Range, _ = strconv.Atoi(query.Get("range"))
	params.Limit, _ = strconv.Atoi(query.Get("limit"))

	if params.Limit <= 0 {
		params.Limit = 100
	}

	results, err := SearchEvents(db, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if results == nil {
		w.Write([]byte("[]"))
	} else {
		json.NewEncoder(w).Encode(results)
	}
}

func serveTiles(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	z, _ := strconv.Atoi(parts[2])
	x, _ := strconv.Atoi(parts[3])
	yStr := strings.TrimSuffix(parts[4], ".pbf")
	y, _ := strconv.Atoi(yStr)

	tileData, err := GetTileData(db, z, x, y)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if len(tileData) > 2 {
		if tileData[0] == 0x1f && tileData[1] == 0x8b {
			w.Header().Set("Content-Encoding", "gzip")
		} else if tileData[0] == 0x78 {
			w.Header().Set("Content-Encoding", "deflate")
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(tileData)
}
