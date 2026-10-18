// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"errors"
	"fmt"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) SearchEntityLabels(searchQuery string, limit int, searchParams ...SearchParams) ([]SearchResult, error) {
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
		WITH matched AS (
			SELECT 
				rowid AS entity_id, 
				bm25(entity_label_search) AS power,
				snippet(entity_label_search, 0, '<mark>', '</mark>', '...', 16) AS snip
			FROM entity_label_search
			WHERE entity_label_search MATCH ?
		)
		SELECT
			m.entity_id,
			COALESCE(e.title, 'Q' || m.entity_id) AS title,
			COALESCE(e.description, '') AS description,
			COALESCE(e.article_id, 0),
			m.snip,
			m.power
		FROM matched m
		LEFT JOIN entities e ON m.entity_id = e.id
		ORDER BY m.power ASC
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
			title := stmt.ColumnText(1)
			desc := stmt.ColumnText(2)
			snip := desc
			if snip == "" {
				snip = stmt.ColumnText(4)
			}
			if snip == "" {
				snip = title
			}

			res := SearchResult{
				EntityID:  entID,
				ArticleID: int(stmt.ColumnInt64(3)),
				Title:     title,
				Text:      desc,
				Snippet:   snip,
				Power:     normalizeBM25(stmt.ColumnFloat(5)),
				Lat:       info.Lat,
				Lon:       info.Lon,
				Year:      info.YearBegin,
				Code:      matchCode,
				DateBegin: info.DateBegin,
				DateEnd:   info.DateEnd,
				Times:     info.Times,
				Places:    info.Places,
				Type:      "T",
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
