// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package event

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"cronotopia/atlas"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func SearchEvents(conn *sqlite.Conn, p SearchParams) ([]EventResult, error) {
	hasSpace := p.HasSpace || (p.Lat != 0 || p.Lon != 0)
	hasTime := p.HasTime || (p.Year != 0 || p.Month != 0 || p.Day != 0)

	rtreeSchema := TableSchema(conn, "entity_places_rtree")
	placeSchema := TableSchema(conn, "entity_places")
	timeSchema := TableSchema(conn, "entity_times")
	entSchema := TableSchema(conn, "entities")

	if hasSpace && !HasSchemaTable(conn, rtreeSchema, "entity_places_rtree") {
		hasSpace = false
	}
	if hasTime && !HasSchemaTable(conn, timeSchema, "entity_times") {
		hasTime = false
	}

	if !hasSpace && !hasTime {
		return []EventResult{}, nil
	}

	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}
	if p.Limit <= 0 {
		p.Limit = 100
	}

	targetJD := GetTargetJulianDay(p)
	candidates := make([]EventResult, 0)
	seenEntity := make(map[int]bool)

	if hasSpace && !hasTime {
		minLat, maxLat, minLon, maxLon := atlas.BoundingBox(p.Lat, p.Lon, p.RadiusKm)
		sqlQuery := fmt.Sprintf(`
			SELECT 
				p.entity_id, p.prop_code, 
				p.latitude, p.longitude, 
				COALESCE(e.title, 'T' || p.entity_id) as label,
				COALESCE(e.description, '') as description,
				COALESCE(e.article_id, 0)
			FROM %s.entity_places_rtree r
			JOIN %s.entity_places p ON r.id = p.id
			LEFT JOIN %s.entities e ON p.entity_id = e.id
			WHERE r.max_lat >= ? AND r.min_lat <= ? AND r.max_lon >= ? AND r.min_lon <= ?
			LIMIT ?
		`, rtreeSchema, placeSchema, entSchema)
		err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
			Args: []any{minLat, maxLat, minLon, maxLon, p.Limit * 8},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				entID := int(stmt.ColumnInt64(0))
				if seenEntity[entID] {
					return nil
				}
				lat := stmt.ColumnFloat(2)
				lon := stmt.ColumnFloat(3)
				dist := atlas.HaversineDistance(p.Lat, p.Lon, lat, lon)
				if dist > p.RadiusKm {
					return nil
				}
				seenEntity[entID] = true
				info := GetEntityInfo(conn, entID, p.Lat, p.Lon)
				candidates = append(candidates, EventResult{
					ID:          entID,
					ArticleID:   int(stmt.ColumnInt64(6)),
					Code:        int(stmt.ColumnInt64(1)),
					Lat:         info.Lat,
					Lon:         info.Lon,
					Year:        info.YearBegin,
					Label:       stmt.ColumnText(4),
					Description: stmt.ColumnText(5),
					DistanceKm:  dist,
					DateBegin:   info.DateBegin,
					DateEnd:     info.DateEnd,
					Times:       info.Times,
					Places:      info.Places,
					Media:       info.Media,
				})
				return nil
			},
		})
		if err != nil {
			return []EventResult{}, err
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].DistanceKm < candidates[j].DistanceKm
		})

	} else if !hasSpace && hasTime {
		var whereClauses []string
		var args []any
		var orderSQL string

		if p.Year != 0 {
			minJD, maxJD := GetJulianDayBounds(p.Year, p.Month, p.Day)
			if p.Range == 0 {
				if minJD == maxJD {
					whereClauses = append(whereClauses, "t.julian_day = ?")
					args = append(args, minJD)
				} else {
					whereClauses = append(whereClauses, "t.julian_day >= ? AND t.julian_day <= ?")
					args = append(args, minJD, maxJD)
				}
				orderSQL = "abs(t.julian_day - ?) ASC"
			} else if p.Range < 0 {
				whereClauses = append(whereClauses, "t.julian_day <= ?")
				args = append(args, minJD)
				orderSQL = "t.julian_day DESC"
			} else if p.Range > 0 {
				whereClauses = append(whereClauses, "t.julian_day >= ?")
				args = append(args, maxJD)
				orderSQL = "t.julian_day ASC"
			}
		} else if p.Month > 0 && p.Day > 0 {
			whereClauses = append(whereClauses, "t.month = ? AND t.day = ?")
			args = append(args, p.Month, p.Day)
			orderSQL = "t.year DESC"
		} else if p.Month > 0 {
			whereClauses = append(whereClauses, "t.month = ?")
			args = append(args, p.Month)
			orderSQL = "t.year DESC"
		} else if p.Day > 0 {
			whereClauses = append(whereClauses, "t.day = ?")
			args = append(args, p.Day)
			orderSQL = "t.year DESC"
		}

		sqlQuery := fmt.Sprintf(`
			SELECT 
				t.entity_id, t.prop_code, 
				t.day, t.month, t.year, t.julian_day,
				COALESCE(e.title, 'T' || t.entity_id) as label,
				COALESCE(e.description, '') as description,
				COALESCE(e.article_id, 0)
			FROM %s.entity_times t
			LEFT JOIN %s.entities e ON t.entity_id = e.id
			WHERE %s
			ORDER BY %s
			LIMIT ?
		`, timeSchema, entSchema, strings.Join(whereClauses, " AND "), orderSQL)

		var finalArgs []any
		finalArgs = append(finalArgs, args...)
		if p.Year != 0 && p.Range == 0 {
			finalArgs = append(finalArgs, targetJD)
		}
		finalArgs = append(finalArgs, p.Limit*8)

		err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
			Args: finalArgs,
			ResultFunc: func(stmt *sqlite.Stmt) error {
				entID := int(stmt.ColumnInt64(0))
				if seenEntity[entID] {
					return nil
				}
				seenEntity[entID] = true
				jd := stmt.ColumnInt64(5)
				var timeDiff int64
				if p.Year != 0 {
					timeDiff = int64(math.Abs(float64(jd - targetJD)))
				}
				info := GetEntityInfo(conn, entID, 0, 0)

				candidates = append(candidates, EventResult{
					ID:          entID,
					ArticleID:   int(stmt.ColumnInt64(8)),
					Code:        int(stmt.ColumnInt64(1)),
					Lat:         info.Lat,
					Lon:         info.Lon,
					Day:         int(stmt.ColumnInt64(2)),
					Month:       int(stmt.ColumnInt64(3)),
					Year:        int(stmt.ColumnInt64(4)),
					JulianDay:   jd,
					Label:       stmt.ColumnText(6),
					Description: stmt.ColumnText(7),
					TimeDiff:    timeDiff,
					DateBegin:   info.DateBegin,
					DateEnd:     info.DateEnd,
					Times:       info.Times,
					Places:      info.Places,
					Media:       info.Media,
				})
				return nil
			},
		})
		if err != nil {
			return []EventResult{}, err
		}

		if p.Year != 0 {
			if p.Range < 0 {
				sort.Slice(candidates, func(i, j int) bool {
					return candidates[i].JulianDay > candidates[j].JulianDay
				})
			} else if p.Range > 0 {
				sort.Slice(candidates, func(i, j int) bool {
					return candidates[i].JulianDay < candidates[j].JulianDay
				})
			} else {
				sort.Slice(candidates, func(i, j int) bool {
					return candidates[i].TimeDiff < candidates[j].TimeDiff
				})
			}
		}

	} else {
		minLat, maxLat, minLon, maxLon := atlas.BoundingBox(p.Lat, p.Lon, p.RadiusKm)
		var timeClauses []string
		var timeArgs []any
		var orderSQL string

		if p.Year != 0 {
			minJD, maxJD := GetJulianDayBounds(p.Year, p.Month, p.Day)
			if p.Range == 0 {
				if minJD == maxJD {
					timeClauses = append(timeClauses, "t.julian_day = ?")
					timeArgs = append(timeArgs, minJD)
				} else {
					timeClauses = append(timeClauses, "t.julian_day >= ? AND t.julian_day <= ?")
					timeArgs = append(timeArgs, minJD, maxJD)
				}
				orderSQL = "abs(t.julian_day - ?) ASC"
			} else if p.Range < 0 {
				timeClauses = append(timeClauses, "t.julian_day <= ?")
				timeArgs = append(timeArgs, minJD)
				orderSQL = "t.julian_day DESC"
			} else if p.Range > 0 {
				timeClauses = append(timeClauses, "t.julian_day >= ?")
				timeArgs = append(timeArgs, maxJD)
				orderSQL = "t.julian_day ASC"
			}
		} else if p.Month > 0 && p.Day > 0 {
			timeClauses = append(timeClauses, "t.month = ? AND t.day = ?")
			timeArgs = append(timeArgs, p.Month, p.Day)
			orderSQL = "t.year DESC"
		} else if p.Month > 0 {
			timeClauses = append(timeClauses, "t.month = ?")
			timeArgs = append(timeArgs, p.Month)
			orderSQL = "t.year DESC"
		} else if p.Day > 0 {
			timeClauses = append(timeClauses, "t.day = ?")
			timeArgs = append(timeArgs, p.Day)
			orderSQL = "t.year DESC"
		}

		whereSQL := "r.max_lat >= ? AND r.min_lat <= ? AND r.max_lon >= ? AND r.min_lon <= ? AND " + strings.Join(timeClauses, " AND ")

		sqlQuery := fmt.Sprintf(`
			SELECT 
				t.entity_id, t.prop_code, 
				p.latitude, p.longitude, 
				t.day, t.month, t.year, t.julian_day,
				COALESCE(e.title, 'T' || t.entity_id) as label,
				COALESCE(e.description, '') as description,
				COALESCE(e.article_id, 0)
			FROM %s.entity_places_rtree r
			JOIN %s.entity_places p ON r.id = p.id
			JOIN %s.entity_times t ON p.entity_id = t.entity_id
			LEFT JOIN %s.entities e ON t.entity_id = e.id
			WHERE %s
			ORDER BY %s
			LIMIT ?
		`, rtreeSchema, placeSchema, timeSchema, entSchema, whereSQL, orderSQL)

		finalArgs := []any{minLat, maxLat, minLon, maxLon}
		finalArgs = append(finalArgs, timeArgs...)
		if p.Year != 0 && p.Range == 0 {
			finalArgs = append(finalArgs, targetJD)
		}
		finalArgs = append(finalArgs, p.Limit*8)

		err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
			Args: finalArgs,
			ResultFunc: func(stmt *sqlite.Stmt) error {
				entID := int(stmt.ColumnInt64(0))
				if seenEntity[entID] {
					return nil
				}
				lat := stmt.ColumnFloat(2)
				lon := stmt.ColumnFloat(3)
				dist := atlas.HaversineDistance(p.Lat, p.Lon, lat, lon)
				if dist > p.RadiusKm {
					return nil
				}
				seenEntity[entID] = true
				jd := stmt.ColumnInt64(7)
				var timeDiff int64
				if p.Year != 0 {
					timeDiff = int64(math.Abs(float64(jd - targetJD)))
				}
				info := GetEntityInfo(conn, entID, p.Lat, p.Lon)

				candidates = append(candidates, EventResult{
					ID:          entID,
					ArticleID:   int(stmt.ColumnInt64(10)),
					Code:        int(stmt.ColumnInt64(1)),
					Lat:         info.Lat,
					Lon:         info.Lon,
					Day:         int(stmt.ColumnInt64(4)),
					Month:       int(stmt.ColumnInt64(5)),
					Year:        int(stmt.ColumnInt64(6)),
					JulianDay:   jd,
					Label:       stmt.ColumnText(8),
					Description: stmt.ColumnText(9),
					DistanceKm:  dist,
					TimeDiff:    timeDiff,
					DateBegin:   info.DateBegin,
					DateEnd:     info.DateEnd,
					Times:       info.Times,
					Places:      info.Places,
					Media:       info.Media,
				})
				return nil
			},
		})
		if err != nil {
			return []EventResult{}, err
		}

		if p.Year != 0 {
			if p.Range < 0 {
				sort.Slice(candidates, func(i, j int) bool {
					return candidates[i].JulianDay > candidates[j].JulianDay
				})
			} else if p.Range > 0 {
				sort.Slice(candidates, func(i, j int) bool {
					return candidates[i].JulianDay < candidates[j].JulianDay
				})
			} else {
				sort.Slice(candidates, func(i, j int) bool {
					if candidates[i].DistanceKm != candidates[j].DistanceKm {
						return candidates[i].DistanceKm < candidates[j].DistanceKm
					}
					return candidates[i].TimeDiff < candidates[j].TimeDiff
				})
			}
		} else {
			sort.Slice(candidates, func(i, j int) bool {
				return candidates[i].DistanceKm < candidates[j].DistanceKm
			})
		}
	}

	if len(candidates) > p.Limit {
		candidates = candidates[:p.Limit]
	}
	return candidates, nil
}

func SearchEntityLabels(conn *sqlite.Conn, searchQuery string, limit int, searchParams ...SearchParams) ([]SearchResult, error) {
	sanitized := SanitizeFTSQuery(searchQuery)
	if sanitized == "" {
		return nil, nil
	}

	ftsSchema := TableSchema(conn, "entity_label_search")
	if !HasSchemaTable(conn, ftsSchema, "entity_label_search") {
		return nil, nil
	}

	entSchema := TableSchema(conn, "entities")

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

	sqlQuery := fmt.Sprintf(`
		WITH matched AS (
			SELECT 
				rowid AS entity_id, 
				bm25(entity_label_search) AS power,
				snippet(entity_label_search, 0, '<mark>', '</mark>', '...', 16) AS snip
			FROM %s.entity_label_search
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
		LEFT JOIN %s.entities e ON m.entity_id = e.id
		ORDER BY m.power ASC
		LIMIT ?
	`, ftsSchema, entSchema)

	var results []SearchResult
	seenEntity := make(map[int]bool)

	err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
		Args: []any{sanitized, fetchLimit},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			if len(results) >= limit {
				return ErrDone
			}

			entID := int(stmt.ColumnInt64(0))
			if seenEntity[entID] {
				return nil
			}

			info := GetEntityInfo(conn, entID, p.Lat, p.Lon)
			matchCode := 0
			if hasSpace {
				if !info.HasCoord || atlas.HaversineDistance(p.Lat, p.Lon, info.Lat, info.Lon) > p.RadiusKm {
					return nil
				}
				matchCode = info.Code
			}
			if hasTime {
				matched, code := MatchesTime(conn, entID, p)
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
				Power:     NormalizeBM25(stmt.ColumnFloat(5)),
				Lat:       info.Lat,
				Lon:       info.Lon,
				Year:      info.YearBegin,
				Code:      matchCode,
				DateBegin: info.DateBegin,
				DateEnd:   info.DateEnd,
				Times:     info.Times,
				Places:    info.Places,
				Media:     info.Media,
				Type:      "T",
			}
			results = append(results, res)

			if len(results) >= limit {
				return ErrDone
			}
			return nil
		},
	})

	if err != nil && !errors.Is(err, ErrDone) {
		return nil, err
	}
	return results, nil
}
