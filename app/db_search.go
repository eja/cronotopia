// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type SearchParams struct {
	Lang             string
	QueryText        string
	Day, Month, Year int
	Lat, Lon         float64
	RadiusKm         float64
	Range            int // -1 = Before (< T), 0 = Exact (= T), +1 = After (> T)
	Limit            int
}

type EventResult struct {
	ID          int     `json:"id"`
	ArticleID   int     `json:"article_id,omitempty"`
	Code        int     `json:"code"`
	Lat         float64 `json:"latitude"`
	Lon         float64 `json:"longitude"`
	Day         int     `json:"day"`
	Month       int     `json:"month"`
	Year        int     `json:"year"`
	JulianDay   int64   `json:"julian_day,omitempty"`
	Label       string  `json:"label"`
	Description string  `json:"description,omitempty"`
	DistanceKm  float64 `json:"distance_km,omitempty"`
	TimeDiff    int64   `json:"time_diff,omitempty"`
	DateBegin   string  `json:"date_begin,omitempty"`
	DateEnd     string  `json:"date_end,omitempty"`
}

type SearchResult struct {
	ArticleID int     `json:"article_id,omitempty"`
	EntityID  int     `json:"entity_id,omitempty"`
	Title     string  `json:"title,omitempty"`
	Text      string  `json:"text"`
	Type      string  `json:"type,omitempty"` // T=Title, C=Content, V=Vector, E=Event
	Power     float64 `json:"power"`
	Snippet   string  `json:"snippet"`
	Lat       float64 `json:"latitude,omitempty"`
	Lon       float64 `json:"longitude,omitempty"`
	Year      int     `json:"year,omitempty"`
	Code      int     `json:"code,omitempty"` // Wikidata property code
	DateBegin string  `json:"date_begin,omitempty"`
	DateEnd   string  `json:"date_end,omitempty"`
}

type EntityInfo struct {
	Lat       float64
	Lon       float64
	HasCoord  bool
	Code      int
	DateBegin string
	DateEnd   string
	YearBegin int
	YearEnd   int
}

func (h *DBHandler) GetEntityInfo(conn *sqlite.Conn, entityID int, targetLat, targetLon float64) EntityInfo {
	var info EntityInfo
	if entityID <= 0 {
		return info
	}

	type placeRow struct {
		code int
		lat  float64
		lon  float64
	}
	var places []placeRow
	_ = sqlitex.Execute(conn, "SELECT prop_code, latitude, longitude FROM entity_places WHERE entity_id = ?", &sqlitex.ExecOptions{
		Args: []any{entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			places = append(places, placeRow{
				code: int(stmt.ColumnInt64(0)),
				lat:  stmt.ColumnFloat(1),
				lon:  stmt.ColumnFloat(2),
			})
			return nil
		},
	})

	if len(places) > 0 {
		info.HasCoord = true
		if targetLat != 0 || targetLon != 0 {
			bestDist := math.MaxFloat64
			for _, p := range places {
				d := HaversineDistance(targetLat, targetLon, p.lat, p.lon)
				if d < bestDist {
					bestDist = d
					info.Lat = p.lat
					info.Lon = p.lon
					info.Code = p.code
				}
			}
		} else {
			info.Lat = places[0].lat
			info.Lon = places[0].lon
			info.Code = places[0].code
			for _, p := range places {
				if p.code == 625 {
					info.Lat = p.lat
					info.Lon = p.lon
					info.Code = 625
					break
				}
			}
		}
	}

	type timeRow struct {
		code               int
		y, m, d, h, min, s int
		jd                 int64
	}
	var times []timeRow
	_ = sqlitex.Execute(conn, "SELECT prop_code, year, month, day, hour, minute, second, julian_day FROM entity_times WHERE entity_id = ? ORDER BY julian_day ASC", &sqlitex.ExecOptions{
		Args: []any{entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			times = append(times, timeRow{
				code: int(stmt.ColumnInt64(0)),
				y:    int(stmt.ColumnInt64(1)),
				m:    int(stmt.ColumnInt64(2)),
				d:    int(stmt.ColumnInt64(3)),
				h:    int(stmt.ColumnInt64(4)),
				min:  int(stmt.ColumnInt64(5)),
				s:    int(stmt.ColumnInt64(6)),
				jd:   stmt.ColumnInt64(7),
			})
			return nil
		},
	})

	if len(times) > 0 {
		first := times[0]
		last := times[len(times)-1]
		info.YearBegin = first.y
		info.YearEnd = last.y
		info.DateBegin = FormatDateTime(first.y, first.m, first.d, first.h, first.min, first.s)
		info.DateEnd = FormatDateTime(last.y, last.m, last.d, last.h, last.min, last.s)
		if info.Code == 0 {
			info.Code = first.code
		}
	}

	return info
}

func (h *DBHandler) entityMatchesTime(conn *sqlite.Conn, entityID int, p SearchParams) (bool, int) {
	if p.Year == 0 && p.Month == 0 && p.Day == 0 {
		return true, 0
	}
	targetJD := DateToJulianDay(p.Year, p.Month, p.Day)
	var matched bool
	var propCode int
	var query string
	var args []any

	if p.Year != 0 {
		if p.Range == 0 {
			if p.Month > 0 && p.Day > 0 {
				query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND year = ? AND month = ? AND day = ? LIMIT 1"
				args = []any{entityID, p.Year, p.Month, p.Day}
			} else if p.Month > 0 {
				query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND year = ? AND month = ? LIMIT 1"
				args = []any{entityID, p.Year, p.Month}
			} else {
				query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND year = ? LIMIT 1"
				args = []any{entityID, p.Year}
			}
		} else if p.Range < 0 {
			query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND julian_day <= ? ORDER BY julian_day DESC LIMIT 1"
			args = []any{entityID, targetJD}
		} else if p.Range > 0 {
			query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND julian_day >= ? ORDER BY julian_day ASC LIMIT 1"
			args = []any{entityID, targetJD}
		}
	} else if p.Month > 0 && p.Day > 0 {
		query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND month = ? AND day = ? LIMIT 1"
		args = []any{entityID, p.Month, p.Day}
	} else if p.Month > 0 {
		query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND month = ? LIMIT 1"
		args = []any{entityID, p.Month}
	} else if p.Day > 0 {
		query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND day = ? LIMIT 1"
		args = []any{entityID, p.Day}
	}

	_ = sqlitex.Execute(conn, query, &sqlitex.ExecOptions{
		Args: args,
		ResultFunc: func(stmt *sqlite.Stmt) error {
			matched = true
			propCode = int(stmt.ColumnInt64(0))
			return nil
		},
	})
	return matched, propCode
}

func Snippet(val string, maxLen int) string {
	if utf8.RuneCountInString(val) > maxLen {
		runes := []rune(val)
		return string(runes[:maxLen]) + "..."
	}
	return val
}

func normalizeBM25(score float64) float64 {
	rawScore := -score
	if rawScore < 0 {
		rawScore = 0
	}
	const c = 0.1
	return (1.0 - math.Exp(-c*rawScore)) * 100.0
}

func sanitizeFTSQuery(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	var sanitized []string
	for _, w := range words {
		clean := strings.ReplaceAll(w, `"`, `""`)
		sanitized = append(sanitized, `"`+clean+`"`)
	}
	return strings.Join(sanitized, " ")
}

func (h *DBHandler) SearchEvents(p SearchParams) ([]EventResult, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return []EventResult{}, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	hasSpace := p.Lat != 0 || p.Lon != 0
	hasTime := p.Year != 0 || p.Month != 0 || p.Day != 0

	if !hasSpace && !hasTime {
		return []EventResult{}, nil
	}

	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}
	if p.Limit <= 0 {
		p.Limit = 100
	}
	if p.Lang == "" {
		p.Lang = "en"
	}

	targetJD := DateToJulianDay(p.Year, p.Month, p.Day)
	candidates := make([]EventResult, 0)
	seenEntity := make(map[int]bool)

	if hasSpace && !hasTime {
		minLat, maxLat, minLon, maxLon := BoundingBox(p.Lat, p.Lon, p.RadiusKm)
		sqlQuery := `
			SELECT 
				p.entity_id, p.prop_code, 
				p.latitude, p.longitude, 
				COALESCE(
					l.label,
					(SELECT label FROM entity_labels WHERE entity_id = p.entity_id AND lang = 'en' LIMIT 1),
					(SELECT label FROM entity_labels WHERE entity_id = p.entity_id LIMIT 1),
					e.article_title,
					'Q' || p.entity_id
				) as label,
				COALESCE(
					l.description,
					(SELECT description FROM entity_labels WHERE entity_id = p.entity_id AND lang = 'en' LIMIT 1),
					(SELECT description FROM entity_labels WHERE entity_id = p.entity_id LIMIT 1),
					''
				) as description,
				COALESCE(e.article_id, 0)
			FROM entity_places p
			LEFT JOIN entities e ON p.entity_id = e.id
			LEFT JOIN entity_labels l ON p.entity_id = l.entity_id AND l.lang = ?
			WHERE p.latitude >= ? AND p.latitude <= ? AND p.longitude >= ? AND p.longitude <= ?
			LIMIT ?
		`
		err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
			Args: []any{p.Lang, minLat, maxLat, minLon, maxLon, p.Limit * 8},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				entID := int(stmt.ColumnInt64(0))
				if seenEntity[entID] {
					return nil
				}
				lat := stmt.ColumnFloat(2)
				lon := stmt.ColumnFloat(3)
				dist := HaversineDistance(p.Lat, p.Lon, lat, lon)
				if dist > p.RadiusKm {
					return nil
				}
				seenEntity[entID] = true
				info := h.GetEntityInfo(conn, entID, p.Lat, p.Lon)
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
			if p.Range == 0 {
				if p.Month > 0 && p.Day > 0 {
					whereClauses = append(whereClauses, "t.year = ? AND t.month = ? AND t.day = ?")
					args = append(args, p.Year, p.Month, p.Day)
				} else if p.Month > 0 {
					whereClauses = append(whereClauses, "t.year = ? AND t.month = ?")
					args = append(args, p.Year, p.Month)
				} else {
					whereClauses = append(whereClauses, "t.year = ?")
					args = append(args, p.Year)
				}
				orderSQL = "abs(t.julian_day - ?) ASC"
			} else if p.Range < 0 {
				whereClauses = append(whereClauses, "t.julian_day <= ?")
				args = append(args, targetJD)
				orderSQL = "t.julian_day DESC"
			} else if p.Range > 0 {
				whereClauses = append(whereClauses, "t.julian_day >= ?")
				args = append(args, targetJD)
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
				COALESCE(
					l.label,
					(SELECT label FROM entity_labels WHERE entity_id = t.entity_id AND lang = 'en' LIMIT 1),
					(SELECT label FROM entity_labels WHERE entity_id = t.entity_id LIMIT 1),
					e.article_title,
					'Q' || t.entity_id
				) as label,
				COALESCE(
					l.description,
					(SELECT description FROM entity_labels WHERE entity_id = t.entity_id AND lang = 'en' LIMIT 1),
					(SELECT description FROM entity_labels WHERE entity_id = t.entity_id LIMIT 1),
					''
				) as description,
				COALESCE(e.article_id, 0)
			FROM entity_times t
			LEFT JOIN entities e ON t.entity_id = e.id
			LEFT JOIN entity_labels l ON t.entity_id = l.entity_id AND l.lang = ?
			WHERE %s
			ORDER BY %s
			LIMIT ?
		`, strings.Join(whereClauses, " AND "), orderSQL)

		finalArgs := []any{p.Lang}
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
				info := h.GetEntityInfo(conn, entID, 0, 0)

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
		minLat, maxLat, minLon, maxLon := BoundingBox(p.Lat, p.Lon, p.RadiusKm)
		var timeClauses []string
		var timeArgs []any
		var orderSQL string

		if p.Year != 0 {
			if p.Range == 0 {
				if p.Month > 0 && p.Day > 0 {
					timeClauses = append(timeClauses, "t.year = ? AND t.month = ? AND t.day = ?")
					timeArgs = append(timeArgs, p.Year, p.Month, p.Day)
				} else if p.Month > 0 {
					timeClauses = append(timeClauses, "t.year = ? AND t.month = ?")
					timeArgs = append(timeArgs, p.Year, p.Month)
				} else {
					timeClauses = append(timeClauses, "t.year = ?")
					timeArgs = append(timeArgs, p.Year)
				}
				orderSQL = "abs(t.julian_day - ?) ASC"
			} else if p.Range < 0 {
				timeClauses = append(timeClauses, "t.julian_day <= ?")
				timeArgs = append(timeArgs, targetJD)
				orderSQL = "t.julian_day DESC"
			} else if p.Range > 0 {
				timeClauses = append(timeClauses, "t.julian_day >= ?")
				timeArgs = append(timeArgs, targetJD)
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

		whereSQL := "p.latitude >= ? AND p.latitude <= ? AND p.longitude >= ? AND p.longitude <= ? AND " + strings.Join(timeClauses, " AND ")

		sqlQuery := fmt.Sprintf(`
			SELECT 
				t.entity_id, t.prop_code, 
				p.latitude, p.longitude, 
				t.day, t.month, t.year, t.julian_day,
				COALESCE(
					l.label,
					(SELECT label FROM entity_labels WHERE entity_id = t.entity_id AND lang = 'en' LIMIT 1),
					(SELECT label FROM entity_labels WHERE entity_id = t.entity_id LIMIT 1),
					e.article_title,
					'Q' || t.entity_id
				) as label,
				COALESCE(
					l.description,
					(SELECT description FROM entity_labels WHERE entity_id = t.entity_id AND lang = 'en' LIMIT 1),
					(SELECT description FROM entity_labels WHERE entity_id = t.entity_id LIMIT 1),
					''
				) as description,
				COALESCE(e.article_id, 0)
			FROM entity_places p
			JOIN entity_times t ON p.entity_id = t.entity_id
			LEFT JOIN entities e ON t.entity_id = e.id
			LEFT JOIN entity_labels l ON t.entity_id = l.entity_id AND l.lang = ?
			WHERE %s
			ORDER BY %s
			LIMIT ?
		`, whereSQL, orderSQL)

		finalArgs := []any{p.Lang, minLat, maxLat, minLon, maxLon}
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
				dist := HaversineDistance(p.Lat, p.Lon, lat, lon)
				if dist > p.RadiusKm {
					return nil
				}
				seenEntity[entID] = true
				jd := stmt.ColumnInt64(7)
				var timeDiff int64
				if p.Year != 0 {
					timeDiff = int64(math.Abs(float64(jd - targetJD)))
				}
				info := h.GetEntityInfo(conn, entID, p.Lat, p.Lon)

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
			SELECT pl.entity_id FROM entity_places pl
			WHERE pl.latitude >= ? AND pl.latitude <= ? AND pl.longitude >= ? AND pl.longitude <= ?
		)`)
		args = append(args, minLat, maxLat, minLon, maxLon)
	}

	if hasTime {
		if p.Year != 0 {
			targetJD := DateToJulianDay(p.Year, p.Month, p.Day)
			if p.Range == 0 {
				if p.Month > 0 && p.Day > 0 {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.year = ? AND tm.month = ? AND tm.day = ?)`)
					args = append(args, p.Year, p.Month, p.Day)
				} else if p.Month > 0 {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.year = ? AND tm.month = ?)`)
					args = append(args, p.Year, p.Month)
				} else {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.year = ?)`)
					args = append(args, p.Year)
				}
			} else if p.Range < 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day <= ?)`)
				args = append(args, targetJD)
			} else if p.Range > 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day >= ?)`)
				args = append(args, targetJD)
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
				Type:      "C",
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
	hasSpace := p.Lat != 0 || p.Lon != 0
	hasTime := p.Year != 0 || p.Month != 0 || p.Day != 0
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}

	var countVectors int
	_ = sqlitex.Execute(conn, "SELECT COUNT(*) FROM vectors", &sqlitex.ExecOptions{
		ResultFunc: func(s *sqlite.Stmt) error { countVectors = int(s.ColumnInt64(0)); return nil },
	})

	if !h.AiHasANN() && countVectors == 0 {
		return []SearchResult{}, nil
	}

	fullQuery := options.aiModelPrefixSearch + query
	queryEmbedding, err := aiEmbeddings(fullQuery)
	if err != nil {
		log.Printf("SearchVectors: aiEmbeddings error: %v", err)
		return []SearchResult{}, err
	}

	candidateLimit := limit * 4
	if candidateLimit < 50 {
		candidateLimit = 50
	}

	topResults := make([]VectorDistance, 0, candidateLimit)

	if h.AiHasANN() {
		topAnnResults, err := h.SearchAnn(queryEmbedding, options.aiAnnSize, candidateLimit)
		if err != nil {
			log.Printf("SearchVectors: SearchAnn error: %v", err)
			return []SearchResult{}, err
		}

		var ids []string
		for _, v := range topAnnResults {
			var vectorID int64
			_ = sqlitex.Execute(conn, "SELECT vectors_id FROM vectors_ann_index WHERE chunk_id = ? AND chunk_position = ? LIMIT 1", &sqlitex.ExecOptions{
				Args: []any{v.ChunkRowID, v.ChunkPosition},
				ResultFunc: func(stmt *sqlite.Stmt) error {
					vectorID = stmt.ColumnInt64(0)
					return nil
				},
			})
			if vectorID > 0 {
				topResults = append(topResults, VectorDistance{ID: vectorID, Distance: v.Distance})
				ids = append(ids, strconv.FormatInt(vectorID, 10))
			}
		}

		if countVectors > 0 && len(ids) > 0 {
			rerankMap := make(map[int64]float32)
			sqlQuery := "SELECT id, embedding FROM vectors WHERE id IN (" + strings.Join(ids, ",") + ")"
			_ = sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
				ResultFunc: func(stmt *sqlite.Stmt) error {
					id := stmt.ColumnInt64(0)
					bLen := stmt.ColumnLen(1)
					buf := make([]byte, bLen)
					stmt.ColumnBytes(1, buf)
					fBuf := BytesToFloat32(buf)
					if len(fBuf) == len(queryEmbedding) {
						rerankMap[id] = dot(queryEmbedding, fBuf)
					}
					return nil
				},
			})
			for i := range topResults {
				if fullSim, ok := rerankMap[topResults[i].ID]; ok {
					topResults[i].Distance = fullSim
				}
			}
		}
	} else if countVectors > 0 {
		var floatBuf []float32
		_ = sqlitex.Execute(conn, "SELECT id, embedding FROM vectors", &sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				id := stmt.ColumnInt64(0)
				bLen := stmt.ColumnLen(1)
				buf := make([]byte, bLen)
				stmt.ColumnBytes(1, buf)
				floatBuf = BytesToFloat32(buf)

				if len(floatBuf) != len(queryEmbedding) {
					return nil
				}
				sim := dot(queryEmbedding, floatBuf)
				if len(topResults) < candidateLimit {
					topResults = append(topResults, VectorDistance{ID: id, Distance: sim})
				} else {
					minIdx := -1
					minSim := float32(2.0)
					for i := range topResults {
						if topResults[i].Distance < minSim {
							minSim = topResults[i].Distance
							minIdx = i
						}
					}
					if minIdx >= 0 && sim > minSim {
						topResults[minIdx] = VectorDistance{ID: id, Distance: sim}
					}
				}
				return nil
			},
		})
	}

	sort.Slice(topResults, func(i, j int) bool {
		return topResults[i].Distance > topResults[j].Distance
	})

	results := make([]SearchResult, 0, limit)
	seenEntity := make(map[int]bool)

	for _, vd := range topResults {
		var res SearchResult
		query := `
			SELECT s.entity_id, COALESCE(e.article_title, s.title), s.content, COALESCE(e.article_id, 0)
			FROM sections s
			LEFT JOIN entities e ON s.entity_id = e.id
			WHERE s.id = ?
		`
		_ = sqlitex.Execute(conn, query, &sqlitex.ExecOptions{
			Args: []any{vd.ID},
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
		res.Type = "V"
		res.Snippet = Snippet(res.Text, 160)
		res.Power = math.Max(0, math.Min(100, float64((vd.Distance+1.0)/2.0*100.0)))
		results = append(results, res)

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}
