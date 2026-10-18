// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"log"
	"math"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) SearchVectors(query string, limit int, searchParams ...SearchParams) ([]SearchResult, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return []SearchResult{}, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	var p SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}
	hasSpace := p.HasSpace || (p.Lat != 0 || p.Lon != 0)
	hasTime := p.HasTime || (p.Year != 0 || p.Month != 0 || p.Day != 0)
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}
	if limit <= 0 {
		limit = 10
	}

	if !h.AiHasANN() {
		return []SearchResult{}, nil
	}

	fullQuery := options.aiModelPrefixSearch + query
	queryEmbedding, err := aiEmbeddings(fullQuery)
	if err != nil {
		log.Printf("SearchVectors: aiEmbeddings error: %v", err)
		return []SearchResult{}, err
	}

	candidateLimit := limit * 6
	if candidateLimit < 60 {
		candidateLimit = 60
	}
	if hasSpace || hasTime {
		candidateLimit = MaxSearchCandidates
	}

	topAnnResults, err := h.SearchAnn(queryEmbedding, options.aiAnnSize, candidateLimit)
	if err != nil {
		log.Printf("SearchVectors: SearchAnn error: %v", err)
		return []SearchResult{}, err
	}

	results := make([]SearchResult, 0, limit)
	seenEntity := make(map[int]bool)

	for _, v := range topAnnResults {
		var vectorID int64
		_ = sqlitex.Execute(conn, "SELECT vectors_id FROM vectors_ann_index WHERE chunk_id = ? AND chunk_position = ? LIMIT 1", &sqlitex.ExecOptions{
			Args: []any{v.ChunkRowID, v.ChunkPosition},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				vectorID = stmt.ColumnInt64(0)
				return nil
			},
		})
		if vectorID == 0 {
			continue
		}

		var res SearchResult
		querySQL := `
			SELECT s.entity_id, COALESCE(e.title, s.title), s.content, COALESCE(e.article_id, 0)
			FROM sections s
			LEFT JOIN entities e ON s.entity_id = e.id
			WHERE s.id = ?
		`
		_ = sqlitex.Execute(conn, querySQL, &sqlitex.ExecOptions{
			Args: []any{vectorID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				res.EntityID = int(stmt.ColumnInt64(0))
				res.Title = stmt.ColumnText(1)
				res.Text = stmt.ColumnText(2)
				res.ArticleID = int(stmt.ColumnInt64(3))
				return nil
			},
		})

		if res.EntityID == 0 || seenEntity[res.EntityID] {
			continue
		}

		info := h.GetEntityInfo(conn, res.EntityID, p.Lat, p.Lon)
		matchCode := 0
		if hasSpace {
			if !info.HasCoord || HaversineDistance(p.Lat, p.Lon, info.Lat, info.Lon) > p.RadiusKm {
				continue
			}
			matchCode = info.Code
		}
		if hasTime {
			matched, code := h.entityMatchesTime(conn, res.EntityID, p)
			if !matched {
				continue
			}
			matchCode = code
		}

		seenEntity[res.EntityID] = true
		res.Lat = info.Lat
		res.Lon = info.Lon
		res.Year = info.YearBegin
		res.Code = matchCode
		res.DateBegin = info.DateBegin
		res.DateEnd = info.DateEnd
		res.Times = info.Times
		res.Places = info.Places
		res.Type = "S"
		res.Snippet = Snippet(res.Text, 160)
		res.Power = math.Max(0, math.Min(100, float64((v.Distance+1.0)/2.0*100.0)))
		results = append(results, res)

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}
