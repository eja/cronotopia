// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"cronotopia/acta"
	"cronotopia/atlas"
	"cronotopia/corpus"
	"cronotopia/media"
	"cronotopia/vfs"
	"zombiezen.com/go/sqlite"
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

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
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

		conn := s.pool.Get(r.Context())
		if conn == nil {
			http.Error(w, "Database busy", http.StatusServiceUnavailable)
			return
		}
		defer s.pool.Put(conn)

		resp := s.processMCPCall(conn, req)
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

func (s *Server) processMCPCall(conn *sqlite.Conn, req jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
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
								"description": "Search mode: 'hybrid', 'entity', 'lexical', or 'semantic'.",
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
					"description": "Retrieve centroids, bounding boxes, and metadata of vector map features around given coordinates and radius.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"latitude":  map[string]any{"type": "number"},
							"longitude": map[string]any{"type": "number"},
							"radius":    map[string]any{"type": "number", "default": 1000},
							"zoom":      map[string]any{"type": "integer"},
							"limit":     map[string]any{"type": "integer", "default": 50},
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
							"id":        map[string]any{"type": "string"},
							"latitude":  map[string]any{"type": "number"},
							"longitude": map[string]any{"type": "number"},
							"radius":    map[string]any{"type": "number", "default": 1000},
							"zoom":      map[string]any{"type": "integer"},
						},
						"required": []string{"id", "latitude", "longitude"},
					},
				},
				{
					"name":        "get_media",
					"description": "Retrieve media content by media ID with optional offset and length.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":     map[string]any{"type": "integer"},
							"offset": map[string]any{"type": "integer"},
							"length": map[string]any{"type": "integer"},
						},
						"required": []string{"id"},
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
		resp.Result = s.executeMCPTool(conn, p.Name, p.Arguments)
	default:
		resp.Error = map[string]any{"code": -32601, "message": "Method not found"}
	}
	return resp
}

func (s *Server) executeMCPTool(conn *sqlite.Conn, name string, args map[string]any) map[string]any {
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

		events, err := acta.SearchEvents(conn, acta.SearchParams{
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
			events = []acta.EventResult{}
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

		var results []acta.SearchResult
		var err error

		candidateLimit := limit * 6
		if candidateLimit < 60 {
			candidateLimit = 60
		}

		if mode == "semantic" {
			if s.engine == nil {
				return map[string]any{
					"isError": true,
					"content": []map[string]any{{"type": "text", "text": "Semantic search unavailable: AI model is not loaded"}},
				}
			}
			raw, err := corpus.SearchVectors(conn, s.engine, q, candidateLimit)
			if err != nil {
				return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
			}
			results = acta.EnrichMatches(conn, raw, "S", acta.SearchParams{Limit: limit})
		} else if mode == "lexical" {
			raw, err := corpus.SearchLexical(conn, q, candidateLimit)
			if err != nil {
				return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
			}
			results = acta.EnrichMatches(conn, raw, "L", acta.SearchParams{Limit: limit})
		} else if mode == "entity" {
			results, err = acta.SearchEntityLabels(conn, q, limit)
		} else {
			results, err = acta.SearchHybrid(conn, s.engine, q, limit)
		}

		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if results == nil {
			results = []acta.SearchResult{}
		}

		data, _ := json.MarshalIndent(results, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_article":
		var id int
		if f, ok := args["id"].(float64); ok {
			id = int(f)
		} else if str, ok := args["id"].(string); ok {
			id, _ = strconv.Atoi(str)
		}
		art, err := acta.GetArticle(conn, id)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# %s (ID: %d)\n\n", art.Title, art.EntityID))
		for _, sec := range art.Sections {
			if sec.Title != "" {
				sb.WriteString("## " + sec.Title + "\n")
			}
			sb.WriteString(sec.Content + "\n\n")
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": sb.String()}}}

	case "get_map_config":
		cfg := atlas.GetMapConfig(conn)
		data, _ := json.MarshalIndent(cfg, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_map_centroids":
		lat, _ := args["latitude"].(float64)
		lon, _ := args["longitude"].(float64)
		rad, _ := args["radius"].(float64)
		if rad <= 0 {
			rad = 1000.0
		}

		cfg := atlas.GetMapConfig(conn)
		zoom := cfg.MaxZoom
		if z, ok := args["zoom"].(float64); ok && z > 0 {
			zoom = int(z)
		}

		limit := 50
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}

		mapSchema := vfs.TableSchema(conn, "tiles")
		centroids, err := atlas.ExtractTileCentroids(conn, mapSchema, lat, lon, rad, zoom)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if centroids == nil {
			centroids = []atlas.TileCentroid{}
		}
		if len(centroids) > limit {
			centroids = centroids[:limit]
		}
		data, _ := json.MarshalIndent(centroids, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_map_feature":
		var id string
		if str, ok := args["id"].(string); ok {
			id = str
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

		cfg := atlas.GetMapConfig(conn)
		zoom := cfg.MaxZoom
		if z, ok := args["zoom"].(float64); ok && z > 0 {
			zoom = int(z)
		}

		mapSchema := vfs.TableSchema(conn, "tiles")
		minLat, maxLat, minLon, maxLon := atlas.BoundingBox(lat, lon, rad/1000.0)
		feat, err := atlas.ExtractFeatureByID(conn, mapSchema, id, minLon, minLat, maxLon, maxLat, zoom)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		if feat == nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "Feature not found"}}}
		}
		data, _ := json.MarshalIndent(feat, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}

	case "get_media":
		var id int
		if f, ok := args["id"].(float64); ok {
			id = int(f)
		} else if str, ok := args["id"].(string); ok {
			id, _ = strconv.Atoi(str)
		}
		var offset int64
		if f, ok := args["offset"].(float64); ok {
			offset = int64(f)
		}
		var length int64
		if f, ok := args["length"].(float64); ok {
			length = int64(f)
		}
		mediaSchema := vfs.TableSchema(conn, "media")
		data, _, _, err := media.Get(conn, mediaSchema, id, offset, length)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}
	}
	return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "Tool not found"}}}
}
