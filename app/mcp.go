// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const mcpMaxBody = 4 * 1024 * 1024

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

type mcpSession struct {
	msgCh chan []byte
}

var (
	mcpSessions   = make(map[string]*mcpSession)
	mcpSessionsMu sync.Mutex
)

func handleMCP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("session_id")
	}

	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, mcpMaxBody)

		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad JSON-RPC or payload too large", http.StatusBadRequest)
			return
		}

		if strings.HasPrefix(req.Method, "notifications/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		resp := processMCPCall(req)
		respBytes, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		mcpSessionsMu.Lock()
		sess, ok := mcpSessions[sessionID]
		mcpSessionsMu.Unlock()

		if ok && sess != nil {
			select {
			case sess.msgCh <- respBytes:
				w.WriteHeader(http.StatusAccepted)
			case <-r.Context().Done():
				http.Error(w, "Client disconnected", http.StatusRequestTimeout)
			default:
				http.Error(w, "Session message queue full", http.StatusServiceUnavailable)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
		return
	}

	if r.Method == http.MethodGet {
		if sessionID == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			sessionID = hex.EncodeToString(b)
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		sess := &mcpSession{
			msgCh: make(chan []byte, 64),
		}

		mcpSessionsMu.Lock()
		mcpSessions[sessionID] = sess
		mcpSessionsMu.Unlock()

		defer func() {
			mcpSessionsMu.Lock()
			delete(mcpSessions, sessionID)
			mcpSessionsMu.Unlock()
		}()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /mcp?session_id=%s\n\n", sessionID)
		flusher.Flush()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-sess.msgCh:
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
				flusher.Flush()
			}
		}
	}
}

func processMCPCall(req jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": Name, "version": Version},
		}
	case "notifications/initialized":
		return jsonRPCResponse{}
	case "tools/list":
		resp.Result = map[string]any{
			"tools": []map[string]any{
				{
					"name":        "search_events",
					"description": "Find historical events around given coordinates, radius, and year.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"latitude":  map[string]any{"type": "number"},
							"longitude": map[string]any{"type": "number"},
							"radius":    map[string]any{"type": "number", "default": 50},
							"year":      map[string]any{"type": "integer"},
							"limit":     map[string]any{"type": "integer", "default": 20},
						},
						"required": []string{"latitude", "longitude"},
					},
				},
				{
					"name":        "search_knowledge",
					"description": "Perform lexical, entity label, semantic or hybrid search over Wikipedia articles and Wikidata entities.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{
								"type":        "string",
								"description": "Search text query.",
							},
							"mode": map[string]any{
								"type":        "string",
								"enum":        []string{"hybrid", "entity", "lexical", "semantic"},
								"default":     "hybrid",
								"description": "Search mode: 'hybrid' (default: entity labels + sections + vectors), 'entity' (label index), 'lexical' (BM25 section search), or 'semantic' (vector similarity).",
							},
							"limit": map[string]any{
								"type":    "integer",
								"default": 10,
							},
						},
						"required": []string{"query"},
					},
				},
				{
					"name":        "get_article",
					"description": "Retrieve full Wikipedia article sections by ID or Wikidata QID integer.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "integer"},
						},
						"required": []string{"id"},
					},
				},
				{
					"name":        "get_map_config",
					"description": "Get map configuration including default center coordinates and min/max available zoom levels.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
				{
					"name":        "get_map_centroids",
					"description": "Retrieve centroids, bounding boxes, and metadata of vector map features (e.g. roads, buildings, water, places, POIs) around given coordinates and radius.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"latitude":  map[string]any{"type": "number", "description": "Center latitude."},
							"longitude": map[string]any{"type": "number", "description": "Center longitude."},
							"radius":    map[string]any{"type": "number", "default": 1000, "description": "Search radius in meters (default 1000)."},
							"zoom":      map[string]any{"type": "integer", "description": "Tile zoom level (defaults to map max zoom)."},
							"limit":     map[string]any{"type": "integer", "default": 50, "description": "Maximum number of centroids to return."},
						},
						"required": []string{"latitude", "longitude"},
					},
				},
				{
					"name":        "get_map_feature",
					"description": "Retrieve a specific map feature with its full geometry coordinates and properties by ID around a given location.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":        map[string]any{"type": "string", "description": "Composite feature ID or raw OSM ID."},
							"latitude":  map[string]any{"type": "number", "description": "Latitude near the feature to locate its tile."},
							"longitude": map[string]any{"type": "number", "description": "Longitude near the feature to locate its tile."},
							"radius":    map[string]any{"type": "number", "default": 1000, "description": "Search radius in meters around coordinates (default 1000)."},
							"zoom":      map[string]any{"type": "integer", "description": "Tile zoom level (defaults to map max zoom)."},
						},
						"required": []string{"id", "latitude", "longitude"},
					},
				},
			},
		}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		resp.Result = executeMCPTool(p.Name, p.Arguments)
	default:
		resp.Error = map[string]any{"code": -32601, "message": "Method not found"}
	}
	return resp
}

func executeMCPTool(name string, args map[string]any) map[string]any {
	switch name {
	case "search_events":
		lat, _ := args["latitude"].(float64)
		lon, _ := args["longitude"].(float64)
		rad, _ := args["radius"].(float64)
		yr, _ := args["year"].(float64)
		limit, _ := args["limit"].(float64)

		_, hasLat := args["latitude"]
		_, hasLon := args["longitude"]
		_, hasYear := args["year"]

		events, err := db.SearchEvents(SearchParams{
			Lat:      lat,
			Lon:      lon,
			RadiusKm: rad,
			Year:     int(yr),
			HasSpace: hasLat && hasLon,
			HasTime:  hasYear,
			Limit:    int(limit),
		})
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if events == nil {
			events = []EventResult{}
		}
		data, _ := json.MarshalIndent(events, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "search_knowledge":
		q, _ := args["query"].(string)
		mode, _ := args["mode"].(string)
		if mode == "" {
			mode = "hybrid"
		}

		limit := 10
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}

		var results []SearchResult
		var err error

		if mode == "semantic" {
			if !ai {
				return map[string]any{
					"isError": true,
					"content": []map[string]any{{"type": "text", "text": "Semantic search unavailable: AI model is not loaded"}},
				}
			}
			results, err = db.SearchVectors(q, limit)
		} else if mode == "lexical" {
			results, err = db.SearchLexical(q, limit)
		} else if mode == "entity" {
			results, err = db.SearchEntityLabels(q, limit)
		} else {
			results, err = db.SearchHybrid(q, limit)
		}

		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if results == nil {
			results = []SearchResult{}
		}

		data, _ := json.MarshalIndent(results, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_article":
		var id int
		if f, ok := args["id"].(float64); ok {
			id = int(f)
		} else if s, ok := args["id"].(string); ok {
			id, _ = strconv.Atoi(s)
		}
		art, err := db.ArticleGet(id)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# %s (ID: %d)\n\n", art.Title, art.EntityID))
		for _, s := range art.Sections {
			if s.Title != "" {
				sb.WriteString("## " + s.Title + "\n")
			}
			sb.WriteString(s.Content + "\n\n")
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": sb.String()}}}

	case "get_map_config":
		cfg := db.GetMapConfig()
		data, _ := json.MarshalIndent(cfg, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_map_centroids":
		lat, _ := args["latitude"].(float64)
		lon, _ := args["longitude"].(float64)
		rad, _ := args["radius"].(float64)
		if rad <= 0 {
			rad = 1000.0
		}

		var zoom int
		if z, ok := args["zoom"].(float64); ok && z > 0 {
			zoom = int(z)
		} else {
			zoom = db.GetMapConfig().MaxZoom
		}

		limit := 50
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}

		centroids, err := MapExtractTileCentroids(db, lat, lon, rad, zoom)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if centroids == nil {
			centroids = []MapTileCentroid{}
		}
		if len(centroids) > limit {
			centroids = centroids[:limit]
		}
		data, _ := json.MarshalIndent(centroids, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_map_feature":
		var id string
		if s, ok := args["id"].(string); ok {
			id = s
		} else if num, ok := args["id"].(float64); ok {
			id = strconv.FormatUint(uint64(num), 10)
		}
		if id == "" {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "Feature id is required"}}}
		}

		lat, _ := args["latitude"].(float64)
		lon, _ := args["longitude"].(float64)
		rad, _ := args["radius"].(float64)
		if rad <= 0 {
			rad = 1000.0
		}

		var zoom int
		if z, ok := args["zoom"].(float64); ok && z > 0 {
			zoom = int(z)
		} else {
			zoom = db.GetMapConfig().MaxZoom
		}

		minLat, maxLat, minLon, maxLon := MapBoundingBox(lat, lon, rad/1000.0)
		feat, err := MapExtractFeatureByID(db, id, minLon, minLat, maxLon, maxLat, zoom)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if feat == nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "Feature not found"}}}
		}
		data, _ := json.MarshalIndent(feat, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}
	}
	return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "Tool not found"}}}
}
