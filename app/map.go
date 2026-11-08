// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type MapConfig struct {
	CenterLng float64 `json:"center_lng"`
	CenterLat float64 `json:"center_lat"`
	Zoom      float64 `json:"zoom"`
	MinZoom   int     `json:"min_zoom"`
	MaxZoom   int     `json:"max_zoom"`
}

type MapTileFeature struct {
	ID         string         `json:"id"`
	RawID      uint64         `json:"-"`
	Layer      string         `json:"layer"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   [][][2]float64 `json:"coordinates"`
}

type MapTileCentroid struct {
	ID         string         `json:"id"`
	Layer      string         `json:"layer"`
	Type       string         `json:"type"`
	Height     *float64       `json:"height,omitempty"`
	Elevation  *float64       `json:"elevation,omitempty"`
	Properties map[string]any `json:"properties"`
	Center     [2]float64     `json:"center"`
	BBox       [4]float64     `json:"bbox"`
}

func parseOptionalFloat(val any) *float64 {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case float64:
		return &v
	case float32:
		f := float64(v)
		return &f
	case int:
		f := float64(v)
		return &f
	case int64:
		f := float64(v)
		return &f
	case int32:
		f := float64(v)
		return &f
	case uint:
		f := float64(v)
		return &f
	case uint64:
		f := float64(v)
		return &f
	case uint32:
		f := float64(v)
		return &f
	case string:
		s := strings.TrimSpace(v)
		s = strings.TrimSuffix(s, "m")
		s = strings.TrimSuffix(s, "M")
		s = strings.TrimSpace(s)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return &f
		}
	}
	return nil
}

func extractFloatProperty(props map[string]any, keys ...string) *float64 {
	if props == nil {
		return nil
	}
	for _, k := range keys {
		if val, exists := props[k]; exists {
			if f := parseOptionalFloat(val); f != nil {
				return f
			}
		}
	}
	return nil
}

func handleMap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	q := r.URL.Query()

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
			zoom = db.GetMapConfig().MaxZoom
		}

		minLat, maxLat, minLon, maxLon := MapBoundingBox(lat, lon, rad/1000.0)
		feat, err := MapExtractFeatureByID(db, idStr, minLon, minLat, maxLon, maxLat, zoom)
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
			zoom = db.GetMapConfig().MaxZoom
		}

		centroids, err := MapExtractTileCentroids(db, lat, lon, rad, zoom)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if centroids == nil {
			centroids = []MapTileCentroid{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(centroids)
		return
	}

	cfg := db.GetMapConfig()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cfg)
}

func mapFeatureWithinRadius(feat MapTileFeature, centerLat, centerLon, radiusMeters float64) bool {
	radiusKm := radiusMeters / 1000.0
	for _, ring := range feat.Geometry {
		for _, pt := range ring {
			if MapHaversineDistance(centerLat, centerLon, pt[1], pt[0]) <= radiusKm {
				return true
			}
		}
	}
	return false
}

func mapCalculateCentroidAndBBox(geometry [][][2]float64) ([2]float64, [4]float64) {
	var sumLon, sumLat float64
	var totalPoints float64
	first := true
	var minLon, maxLon, minLat, maxLat float64

	for _, ring := range geometry {
		for _, pt := range ring {
			lon, lat := pt[0], pt[1]
			if first {
				minLon, maxLon = lon, lon
				minLat, maxLat = lat, lat
				first = false
			} else {
				if lon < minLon {
					minLon = lon
				}
				if lon > maxLon {
					maxLon = lon
				}
				if lat < minLat {
					minLat = lat
				}
				if lat > maxLat {
					maxLat = lat
				}
			}
			sumLon += lon
			sumLat += lat
			totalPoints++
		}
	}

	if totalPoints == 0 {
		return [2]float64{0, 0}, [4]float64{}
	}

	center := [2]float64{sumLon / totalPoints, sumLat / totalPoints}
	bbox := [4]float64{minLon, minLat, maxLon, maxLat}

	return center, bbox
}

func MapExtractTileFeatures(h *DBHandler, centerLat, centerLon, radiusMeters float64, zoom int) ([]MapTileFeature, error) {
	if zoom < 0 || zoom > 30 {
		return nil, fmt.Errorf("zoom level out of range: %d", zoom)
	}
	if radiusMeters <= 0 {
		radiusMeters = 1000.0
	}

	minLat, maxLat, minLon, maxLon := MapBoundingBox(centerLat, centerLon, radiusMeters/1000.0)
	x1, y1 := MapLatLonToTile(maxLat, minLon, zoom)
	x2, y2 := MapLatLonToTile(minLat, maxLon, zoom)

	minX, maxX := min(x1, x2), max(x1, x2)
	minY, maxY := min(y1, y2), max(y1, y2)

	featureMap := make(map[string]*MapTileFeature)
	var featureOrder []string

	for tx := minX; tx <= maxX; tx++ {
		for ty := minY; ty <= maxY; ty++ {
			data, err := h.GetTileData(zoom, tx, ty)
			if err != nil || len(data) == 0 {
				continue
			}
			feats, err := MapDecodeMVT(data, tx, ty, zoom)
			if err != nil {
				continue
			}
			for _, f := range feats {
				if mapFeatureWithinRadius(f, centerLat, centerLon, radiusMeters) {
					if existing, exists := featureMap[f.ID]; exists {
						existing.Geometry = append(existing.Geometry, f.Geometry...)
					} else {
						featCopy := f
						featureMap[f.ID] = &featCopy
						featureOrder = append(featureOrder, f.ID)
					}
				}
			}
		}
	}

	matchedFeatures := make([]MapTileFeature, 0, len(featureOrder))
	for _, id := range featureOrder {
		matchedFeatures = append(matchedFeatures, *featureMap[id])
	}

	return matchedFeatures, nil
}

func MapExtractTileCentroids(h *DBHandler, centerLat, centerLon, radiusMeters float64, zoom int) ([]MapTileCentroid, error) {
	features, err := MapExtractTileFeatures(h, centerLat, centerLon, radiusMeters, zoom)
	if err != nil {
		return nil, err
	}

	centroids := make([]MapTileCentroid, len(features))
	for i, f := range features {
		center, bbox := mapCalculateCentroidAndBBox(f.Geometry)
		height := extractFloatProperty(f.Properties, "height", "render_height")
		elevation := extractFloatProperty(f.Properties, "elevation", "ele")
		centroids[i] = MapTileCentroid{
			ID:         f.ID,
			Layer:      f.Layer,
			Type:       f.Type,
			Height:     height,
			Elevation:  elevation,
			Properties: f.Properties,
			Center:     center,
			BBox:       bbox,
		}
	}
	return centroids, nil
}

func mapFeatureMatchesID(f MapTileFeature, queryID string) bool {
	if f.ID == queryID {
		return true
	}
	if parsed, err := strconv.ParseUint(queryID, 10, 64); err == nil && parsed != 0 {
		if f.RawID == parsed {
			return true
		}
	}
	return false
}

func MapExtractFeatureByID(h *DBHandler, id string, minLon, minLat, maxLon, maxLat float64, zoom int) (*MapTileFeature, error) {
	if zoom < 0 || zoom > 30 {
		return nil, fmt.Errorf("zoom level out of range: %d", zoom)
	}

	x1, y1 := MapLatLonToTile(maxLat, minLon, zoom)
	x2, y2 := MapLatLonToTile(minLat, maxLon, zoom)

	minX, maxX := min(x1, x2), max(x1, x2)
	minY, maxY := min(y1, y2), max(y1, y2)

	var found *MapTileFeature

	for tx := minX; tx <= maxX; tx++ {
		for ty := minY; ty <= maxY; ty++ {
			data, err := h.GetTileData(zoom, tx, ty)
			if err != nil || len(data) == 0 {
				continue
			}
			feats, err := MapDecodeMVT(data, tx, ty, zoom)
			if err != nil {
				continue
			}
			for _, f := range feats {
				if mapFeatureMatchesID(f, id) {
					if found == nil {
						featCopy := f
						found = &featCopy
					} else {
						found.Geometry = append(found.Geometry, f.Geometry...)
					}
				}
			}
		}
	}
	return found, nil
}

func (h *DBHandler) GetMapConfig() MapConfig {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return MapConfig{Zoom: 1, MinZoom: 0, MaxZoom: 14}
	}
	defer h.pool.Put(conn)

	config := MapConfig{Zoom: 1, MinZoom: 0, MaxZoom: 14}
	_ = sqlitex.Execute(conn, `
    SELECT 
      MAX(CASE WHEN key = 'map_minzoom' THEN CAST(value AS INTEGER) END),
      MAX(CASE WHEN key = 'map_maxzoom' THEN CAST(value AS INTEGER) END)
    FROM settings
  `, &sqlitex.ExecOptions{
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
	_ = sqlitex.Execute(conn, "SELECT value FROM settings WHERE key = 'map_center'", &sqlitex.ExecOptions{
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
