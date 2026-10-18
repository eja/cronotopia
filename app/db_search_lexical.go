// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"errors"
	"fmt"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) SearchLexical(searchQuery string, limit int, searchParams ...SearchParams) ([]SearchResult, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	sanitized := sanitizeFTSQuery(searchQuery)
	if sanitized == "" {
		return nil, nil
	}

	var p SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}

	hasSpace := p.HasSpace || (p.Lat != 0 || p.Lon != 0)
	hasTime := p.HasTime || (p.Year != 0 || p.Month != 0 || p.Day != 0)
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}

	fetchLimit := limit * 6
	if fetchLimit < 60 {
		fetchLimit = 60
	}
	if hasSpace || hasTime {
		fetchLimit = MaxSearchCandidates
	}

	sqlQuery := `
		SELECT
			s.entity_id,
			COALESCE(e.title, s.title),
			COALESCE(e.article_id, 0),
			s.content,
			snippet(section_search, 1, '<mark>', '</mark>', '...', 32) as snippet,
			bm25(section_search) as power
		FROM section_search
		JOIN sections s ON section_search.rowid = s.id
		LEFT JOIN entities e ON s.entity_id = e.id
		WHERE section_search MATCH ?
		ORDER BY power ASC
		LIMIT ?
	`

	var results []SearchResult
	seenEntity := make(map[int]bool)

	err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
		Args: []any{sanitized, fetchLimit},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			if len(results) >= limit {
				return errDone
			}

			entID := int(stmt.ColumnInt64(0))
			if seenEntity[entID] {
				return nil
			}

			info := h.GetEntityInfo(conn, entID, p.Lat, p.Lon)
			matchCode := 0
			if hasSpace {
				if !info.HasCoord || HaversineDistance(p.Lat, p.Lon, info.Lat, info.Lon) > p.RadiusKm {
					return nil
				}
				matchCode = info.Code
			}
			if hasTime {
				matched, code := h.entityMatchesTime(conn, entID, p)
				if !matched {
					return nil
				}
				matchCode = code
			}

			seenEntity[entID] = true
			res := SearchResult{
				EntityID:  entID,
				ArticleID: int(stmt.ColumnInt64(2)),
				Title:     stmt.ColumnText(1),
				Text:      stmt.ColumnText(3),
				Snippet:   stmt.ColumnText(4),
				Power:     normalizeBM25(stmt.ColumnFloat(5)),
				Lat:       info.Lat,
				Lon:       info.Lon,
				Year:      info.YearBegin,
				Code:      matchCode,
				DateBegin: info.DateBegin,
				DateEnd:   info.DateEnd,
				Times:     info.Times,
				Places:    info.Places,
				Type:      "L",
			}

			if res.Snippet == "" {
				res.Snippet = Snippet(res.Text, 160)
			}
			results = append(results, res)

			if len(results) >= limit {
				return errDone
			}
			return nil
		},
	})

	if err != nil && !errors.Is(err, errDone) {
		return nil, err
	}
	return results, nil
}
