// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const MaxSearchCandidates = 2000

var errDone = errors.New("search limit reached")

type SearchParams struct {
	QueryText        string
	Day, Month, Year int
	Lat, Lon         float64
	RadiusKm         float64
	HasSpace         bool
	HasTime          bool
	Range            int
	Limit            int
}

type EventTime struct {
	Code  int    `json:"code"`
	Year  int    `json:"year"`
	Month int    `json:"month,omitempty"`
	Day   int    `json:"day,omitempty"`
	Date  string `json:"date,omitempty"`
}

type EventPlace struct {
	Code      int     `json:"code"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type EventResult struct {
	ID          int          `json:"id"`
	ArticleID   int          `json:"article_id,omitempty"`
	Code        int          `json:"code"`
	Lat         float64      `json:"latitude"`
	Lon         float64      `json:"longitude"`
	Day         int          `json:"day"`
	Month       int          `json:"month"`
	Year        int          `json:"year"`
	JulianDay   int64        `json:"julian_day,omitempty"`
	Label       string       `json:"label"`
	Description string       `json:"description,omitempty"`
	DistanceKm  float64      `json:"distance_km,omitempty"`
	TimeDiff    int64        `json:"time_diff,omitempty"`
	DateBegin   string       `json:"date_begin,omitempty"`
	DateEnd     string       `json:"date_end,omitempty"`
	Times       []EventTime  `json:"times,omitempty"`
	Places      []EventPlace `json:"places,omitempty"`
}

type SearchResult struct {
	ArticleID int          `json:"article_id,omitempty"`
	EntityID  int          `json:"entity_id,omitempty"`
	Title     string       `json:"title,omitempty"`
	Text      string       `json:"text"`
	Type      string       `json:"type,omitempty"`
	Power     float64      `json:"power"`
	Snippet   string       `json:"snippet"`
	Lat       float64      `json:"latitude,omitempty"`
	Lon       float64      `json:"longitude,omitempty"`
	Year      int          `json:"year,omitempty"`
	Code      int          `json:"code,omitempty"`
	DateBegin string       `json:"date_begin,omitempty"`
	DateEnd   string       `json:"date_end,omitempty"`
	Times     []EventTime  `json:"times,omitempty"`
	Places    []EventPlace `json:"places,omitempty"`
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
	Times     []EventTime
	Places    []EventPlace
}

func getJulianDayBounds(year, month, day int) (int64, int64) {
	if month == 0 && day == 0 {
		return DateToJulianDay(year, 1, 1), DateToJulianDay(year, 12, 31)
	}
	if day == 0 {
		lastDay := 31
		switch month {
		case 2:
			if (year%4 == 0 && year%100 != 0) || (year%400 == 0) {
				lastDay = 29
			} else {
				lastDay = 28
			}
		case 4, 6, 9, 11:
			lastDay = 30
		}
		return DateToJulianDay(year, month, 1), DateToJulianDay(year, month, lastDay)
	}
	jd := DateToJulianDay(year, month, day)
	return jd, jd
}

func getTargetJulianDay(p SearchParams) int64 {
	m := p.Month
	d := p.Day
	if p.Range > 0 && p.Year != 0 {
		if m == 0 && d == 0 {
			m = 12
			d = 31
		} else if d == 0 {
			switch m {
			case 2:
				d = 28
			case 4, 6, 9, 11:
				d = 30
			default:
				d = 31
			}
		}
	}
	return DateToJulianDay(p.Year, m, d)
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
		for _, p := range places {
			info.Places = append(info.Places, EventPlace{
				Code:      p.code,
				Latitude:  p.lat,
				Longitude: p.lon,
			})
		}
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
		code    int
		y, m, d int
		jd      int64
	}
	var times []timeRow
	_ = sqlitex.Execute(conn, "SELECT prop_code, year, month, day, julian_day FROM entity_times WHERE entity_id = ? ORDER BY julian_day ASC", &sqlitex.ExecOptions{
		Args: []any{entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			times = append(times, timeRow{
				code: int(stmt.ColumnInt64(0)),
				y:    int(stmt.ColumnInt64(1)),
				m:    int(stmt.ColumnInt64(2)),
				d:    int(stmt.ColumnInt64(3)),
				jd:   stmt.ColumnInt64(4),
			})
			return nil
		},
	})

	if len(times) > 0 {
		for _, t := range times {
			info.Times = append(info.Times, EventTime{
				Code:  t.code,
				Year:  t.y,
				Month: t.m,
				Day:   t.d,
				Date:  FormatDateTime(t.y, t.m, t.d),
			})
		}
		first := times[0]
		last := times[len(times)-1]
		info.YearBegin = first.y
		info.YearEnd = last.y
		info.DateBegin = FormatDateTime(first.y, first.m, first.d)
		info.DateEnd = FormatDateTime(last.y, last.m, last.d)
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
	var matched bool
	var propCode int
	var query string
	var args []any

	if p.Year != 0 {
		minJD, maxJD := getJulianDayBounds(p.Year, p.Month, p.Day)
		if p.Range == 0 {
			if minJD == maxJD {
				query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND julian_day = ? LIMIT 1"
				args = []any{entityID, minJD}
			} else {
				query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND julian_day >= ? AND julian_day <= ? LIMIT 1"
				args = []any{entityID, minJD, maxJD}
			}
		} else if p.Range < 0 {
			query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND julian_day <= ? ORDER BY julian_day DESC LIMIT 1"
			args = []any{entityID, minJD}
		} else if p.Range > 0 {
			query = "SELECT prop_code FROM entity_times WHERE entity_id = ? AND julian_day >= ? ORDER BY julian_day ASC LIMIT 1"
			args = []any{entityID, maxJD}
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
