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
	id       string
	sendChan chan string
}

var (
	mcpSessions   = make(map[string]*mcpSession)
	mcpSessionsMu sync.RWMutex
)

func handleMCP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("session_id")
	}

	if r.Method == "POST" {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad JSON-RPC", 400)
			return
		}
		if req.Method == "initialize" {
			b := make([]byte, 8)
			rand.Read(b)
			sessionID = hex.EncodeToString(b)
			w.Header().Set("Mcp-Session-Id", sessionID)
		}
		resp := processMCPCall(req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	if r.Method == "GET" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fmt.Fprintf(w, ":connected\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}
}

func processMCPCall(req jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "cronotopia", "version": "9.0.0"},
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
							"radius_km": map[string]any{"type": "number", "default": 50},
							"year":      map[string]any{"type": "integer"},
							"limit":     map[string]any{"type": "integer", "default": 20},
						},
						"required": []string{"latitude", "longitude"},
					},
				},
				{
					"name":        "search_knowledge",
					"description": "Perform lexical, semantic or hybrid search over Wikipedia and Wikidata items.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
							"limit": map[string]any{"type": "integer", "default": 10},
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
		rad, _ := args["radius_km"].(float64)
		yr, _ := args["year"].(float64)
		limit, _ := args["limit"].(float64)

		events, err := db.SearchEvents(SearchParams{
			Lat: lat, Lon: lon, RadiusKm: rad, Year: int(yr), Limit: int(limit),
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
		limit, _ := args["limit"].(float64)
		if limit <= 0 {
			limit = 10
		}
		res, err := db.SearchLexical(q, int(limit))
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
		}
		data, _ := json.MarshalIndent(res, "", "  ")
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
	}
	return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "Tool not found"}}}
}
