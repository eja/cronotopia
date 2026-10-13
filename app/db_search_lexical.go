// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"strings"

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

	hasSpace := p.Lat != 0 || p.Lon != 0
	hasTime := p.Year != 0 || p.Month != 0 || p.Day != 0
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}

	var whereSub []string
	var args []any
	args = append(args, sanitized)

	if hasSpace {
		minLat, maxLat, minLon, maxLon := BoundingBox(p.Lat, p.Lon, p.RadiusKm)
		whereSub = append(whereSub, `s.entity_id IN (
			SELECT p.entity_id FROM entity_places_rtree r
			JOIN entity_places p ON r.id = p.id
			WHERE r.max_lat >= ? AND r.min_lat <= ? AND r.max_lon >= ? AND r.min_lon <= ?
		)`)
		args = append(args, minLat, maxLat, minLon, maxLon)
	}

	if hasTime {
		if p.Year != 0 {
			minJD, maxJD := getJulianDayBounds(p.Year, p.Month, p.Day)
			if p.Range == 0 {
				if minJD == maxJD {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day = ?)`)
					args = append(args, minJD)
				} else {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day >= ? AND tm.julian_day <= ?)`)
					args = append(args, minJD, maxJD)
				}
			} else if p.Range < 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day <= ?)`)
				args = append(args, minJD)
			} else if p.Range > 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day >= ?)`)
				args = append(args, maxJD)
			}
		} else if p.Month > 0 && p.Day > 0 {
			whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.month = ? AND tm.day = ?)`)
			args = append(args, p.Month, p.Day)
		} else if p.Month > 0 {
			whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.month = ?)`)
			args = append(args, p.Month)
		} else if p.Day > 0 {
			whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.day = ?)`)
			args = append(args, p.Day)
		}
	}

	extraWhere := ""
	if len(whereSub) > 0 {
		extraWhere = " AND " + strings.Join(whereSub, " AND ")
	}

	fetchLimit := limit * 4
	if fetchLimit < 40 {
		fetchLimit = 40
	}
	args = append(args, fetchLimit)

	sqlQuery := fmt.Sprintf(`
		SELECT
			s.entity_id,
			COALESCE(e.article_title, s.title),
			COALESCE(e.article_id, 0),
			s.content,
			snippet(section_search, 1, '<mark>', '</mark>', '...', 32) as snippet,
			bm25(section_search) as power
		FROM section_search
		JOIN sections s ON section_search.rowid = s.id
		LEFT JOIN entities e ON s.entity_id = e.id
		WHERE section_search MATCH ? %s
		ORDER BY power ASC
		LIMIT ?
	`, extraWhere)

	var results []SearchResult
	seenEntity := make(map[int]bool)

	err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
		Args: args,
		ResultFunc: func(stmt *sqlite.Stmt) error {
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
			return nil
		},
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, err
}
