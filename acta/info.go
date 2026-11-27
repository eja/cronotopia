// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package acta

import (
	"fmt"
	"math"

	"cronotopia/atlas"
	"cronotopia/media"
	"cronotopia/vfs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func GetEntityInfo(conn *sqlite.Conn, entityID int, targetLat, targetLon float64) EntityInfo {
	var info EntityInfo
	if entityID <= 0 {
		return info
	}

	placeSchema := vfs.TableSchema(conn, "entity_places")
	type placeRow struct {
		code int
		lat  float64
		lon  float64
	}
	var places []placeRow
	_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT prop_code, latitude, longitude FROM %s.entity_places WHERE entity_id = ?", placeSchema), &sqlitex.ExecOptions{
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
				d := atlas.HaversineDistance(targetLat, targetLon, p.lat, p.lon)
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

	timeSchema := vfs.TableSchema(conn, "entity_times")
	type timeRow struct {
		code    int
		y, m, d int
		jd      int64
	}
	var times []timeRow
	_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT prop_code, year, month, day, julian_day FROM %s.entity_times WHERE entity_id = ? ORDER BY julian_day ASC", timeSchema), &sqlitex.ExecOptions{
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

	mediaSchema := vfs.TableSchema(conn, "media")
	if vfs.HasSchemaTable(conn, mediaSchema, "media") {
		if items, err := media.GetByEntity(conn, mediaSchema, entityID); err == nil {
			info.Media = items
		}
	}

	return info
}

func MatchesTime(conn *sqlite.Conn, entityID int, p SearchParams) (bool, int) {
	if p.Year == 0 && p.Month == 0 && p.Day == 0 {
		return true, 0
	}
	var matched bool
	var propCode int
	var query string
	var args []any

	timeSchema := vfs.TableSchema(conn, "entity_times")

	if p.Year != 0 {
		minJD, maxJD := GetJulianDayBounds(p.Year, p.Month, p.Day)
		if p.Range == 0 {
			if minJD == maxJD {
				query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND julian_day = ? LIMIT 1", timeSchema)
				args = []any{entityID, minJD}
			} else {
				query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND julian_day >= ? AND julian_day <= ? LIMIT 1", timeSchema)
				args = []any{entityID, minJD, maxJD}
			}
		} else if p.Range < 0 {
			query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND julian_day <= ? ORDER BY julian_day DESC LIMIT 1", timeSchema)
			args = []any{entityID, minJD}
		} else if p.Range > 0 {
			query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND julian_day >= ? ORDER BY julian_day ASC LIMIT 1", timeSchema)
			args = []any{entityID, maxJD}
		}
	} else if p.Month > 0 && p.Day > 0 {
		query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND month = ? AND day = ? LIMIT 1", timeSchema)
		args = []any{entityID, p.Month, p.Day}
	} else if p.Month > 0 {
		query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND month = ? LIMIT 1", timeSchema)
		args = []any{entityID, p.Month}
	} else if p.Day > 0 {
		query = fmt.Sprintf("SELECT prop_code FROM %s.entity_times WHERE entity_id = ? AND day = ? LIMIT 1", timeSchema)
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
