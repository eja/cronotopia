package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func createTables(db *sql.DB) {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS place (id INTEGER, code INTEGER, latitude REAL, longitude REAL, precision REAL);`,
		`CREATE INDEX IF NOT EXISTS idx_place_id ON place(id);`,

		`CREATE TABLE IF NOT EXISTS time (id INTEGER, code INTEGER, year INTEGER, month INTEGER, day INTEGER, hour INTEGER, minute INTEGER, second INTEGER);`,
		`CREATE INDEX IF NOT EXISTS idx_time_id ON time(id);`,
		`CREATE INDEX IF NOT EXISTS idx_time_ymd ON time(year, month, day);`,

		`CREATE TABLE IF NOT EXISTS query (id INTEGER, language TEXT, label TEXT, data TEXT);`,
		`CREATE INDEX IF NOT EXISTS query_id on query (id);`,
		`CREATE INDEX IF NOT EXISTS query_label on query (label);`,
		`CREATE INDEX IF NOT EXISTS query_lang on query (language);`,

		`CREATE TEMPORARY TABLE IF NOT EXISTS link (id INTEGER, code INTEGER, value INTEGER);`,
		`CREATE INDEX IF NOT EXISTS idx_link_val ON link(value);`,
	}
	for _, s := range sqls {
		if _, err := db.Exec(s); err != nil {
			log.Fatalf("Init DB error: %v", err)
		}
	}
}

func dbWorker(db *sql.DB, in <-chan ExtractedData) {
	stmtTime, err := db.Prepare("INSERT INTO time (id, code, year, month, day, hour, minute, second) VALUES (?,?,?,?,?,?,?,?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmtTime.Close()

	stmtLink, err := db.Prepare("INSERT INTO link (id, code, value) VALUES (?,?,?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmtLink.Close()

	stmtPlace, err := db.Prepare("INSERT INTO place (id, code, latitude, longitude, precision) VALUES (?,?,?,?,?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmtPlace.Close()

	stmtQuery, err := db.Prepare("INSERT INTO query (id, language, label, data) VALUES (?,?,?,?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmtQuery.Close()

	var tx *sql.Tx
	var txStmtTime, txStmtLink, txStmtPlace, txStmtQuery *sql.Stmt

	beginTx := func() {
		var err error
		tx, err = db.Begin()
		if err != nil {
			log.Fatal(err)
		}
		txStmtTime = tx.Stmt(stmtTime)
		txStmtLink = tx.Stmt(stmtLink)
		txStmtPlace = tx.Stmt(stmtPlace)
		txStmtQuery = tx.Stmt(stmtQuery)
	}

	beginTx()

	counter := 0

	for item := range in {
		for _, t := range item.Times {
			_, err := txStmtTime.Exec(item.ID, t.Code, t.Y, t.M, t.D, t.H, t.Min, t.Sec)
			if err != nil {
				log.Printf("Error inserting time: %v", err)
			}
		}
		for _, l := range item.Links {
			_, err := txStmtLink.Exec(item.ID, l.Code, l.Value)
			if err != nil {
				log.Printf("Error inserting link: %v", err)
			}
		}
		for _, p := range item.Place {
			_, err := txStmtPlace.Exec(item.ID, p.Code, p.Lat, p.Lon, p.Precision)
			if err != nil {
				log.Printf("Error inserting place: %v", err)
			}
		}
		for _, q := range item.Query {
			_, err := txStmtQuery.Exec(item.ID, q.Lang, q.Label, q.Data)
			if err != nil {
				log.Printf("Error inserting query: %v", err)
			}
		}

		counter++
		if counter >= BatchSize {
			if err := tx.Commit(); err != nil {
				log.Fatal(err)
			}
			beginTx()
			counter = 0
		}
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}

func postProcess(db *sql.DB) {
	queries := []string{
		`INSERT INTO place SELECT link.id, link.code, place.latitude, place.longitude, place.precision FROM link INNER JOIN place ON link.value = place.id WHERE link.code = 19`,
		`INSERT INTO place SELECT link.id, link.code, place.latitude, place.longitude, place.precision FROM link INNER JOIN place ON link.value = place.id WHERE link.code = 20`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			log.Printf("Post-process warning: %v", err)
		}
	}
}

func SearchEvents(db *sql.DB, p SearchParams) ([]SearchResult, error) {
	var whereClauses []string
	var args []interface{}

	targetDateInt := p.Year*10000 + p.Month*100 + p.Day

	whereClauses = append(whereClauses, "q.language = ?")
	args = append(args, p.Lang)

	if p.Range == 0 {
		if p.Year == 0 {
			if p.Day > 0 && p.Month > 0 {
				whereClauses = append(whereClauses, "t.month = ? AND t.day = ?")
				args = append(args, p.Month, p.Day)
			} else {
				if p.Day > 0 {
					whereClauses = append(whereClauses, "t.day = ?")
					args = append(args, p.Day)
				}
				if p.Month > 0 {
					whereClauses = append(whereClauses, "t.month = ?")
					args = append(args, p.Month)
				}
			}
		} else {
			if p.Day > 0 && p.Month > 0 {
				whereClauses = append(whereClauses, "t.year = ? AND t.month = ? AND t.day = ?")
				args = append(args, p.Year, p.Month, p.Day)
			} else {
				if p.Day > 0 {
					whereClauses = append(whereClauses, "t.year = ? AND t.day = ?")
					args = append(args, p.Year, p.Day)
				} else if p.Month > 0 {
					whereClauses = append(whereClauses, "t.year = ? AND t.month = ?")
					args = append(args, p.Year, p.Month)
				} else {
					whereClauses = append(whereClauses, "t.year = ?")
					args = append(args, p.Year)
				}
			}
		}
	} else if p.Range == 1 {
		whereClauses = append(whereClauses, "(t.year*10000 + t.month*100 + t.day) <= ?")
		args = append(args, targetDateInt)
	} else if p.Range == 2 {
		whereClauses = append(whereClauses, "(t.year*10000 + t.month*100 + t.day) >= ?")
		args = append(args, targetDateInt)
	}

	if p.QueryText != "" {
		words := strings.Fields(p.QueryText)
		for _, w := range words {
			whereClauses = append(whereClauses, "q.label LIKE ?")
			args = append(args, "%"+w+"%")
		}
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	timeOrder := "ASC"
	if p.Range == 1 {
		timeOrder = "DESC"
	}

	sqlQuery := fmt.Sprintf(`
        SELECT 
            t.id, t.code, 
            p.latitude, p.longitude, 
            t.day, t.month, t.year, 
            q.label, q.data,
            (ABS(p.latitude - ?) + ABS(p.longitude - ?)) as spaceSpan,
            ABS((t.year*10000 + t.month*100 + t.day) - ?) as timeSpan
        FROM time t
        JOIN place p ON t.id = p.id
        JOIN query q ON t.id = q.id
        %s
        ORDER BY spaceSpan ASC, timeSpan %s
        LIMIT ?
    `, whereSQL, timeOrder)

	finalArgs := []interface{}{p.Lat, p.Lon, targetDateInt}
	finalArgs = append(finalArgs, args...)
	finalArgs = append(finalArgs, p.Limit)

	rows, err := db.Query(sqlQuery, finalArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var spaceSpan, timeSpan float64

		err := rows.Scan(
			&r.ID, &r.Code,
			&r.Lat, &r.Lon,
			&r.Day, &r.Month, &r.Year,
			&r.Label, &r.Data,
			&spaceSpan, &timeSpan,
		)
		if err != nil {
			continue
		}
		results = append(results, r)
	}

	return results, nil
}

func GetMapConfig(db *sql.DB) MapConfig {
	var err error
	config := MapConfig{Zoom: 1, MinZoom: 0, MaxZoom: 14}

	db.QueryRow("SELECT MIN(zoom_level), MAX(zoom_level) FROM tiles").Scan(&config.MinZoom, &config.MaxZoom)
	config.Zoom = float64(config.MinZoom)
	if config.Zoom == 0 {
		config.Zoom = 1
	}

	var val string
	err = db.QueryRow("SELECT value FROM metadata WHERE name='center'").Scan(&val)
	if err == nil {
		parts := strings.Split(val, ",")
		if len(parts) >= 2 {
			config.CenterLng, _ = strconv.ParseFloat(parts[0], 64)
			config.CenterLat, _ = strconv.ParseFloat(parts[1], 64)
			return config
		}
	}

	var z, x, yTms int
	err = db.QueryRow("SELECT zoom_level, tile_column, tile_row FROM tiles WHERE zoom_level = ? LIMIT 1", config.MinZoom).Scan(&z, &x, &yTms)
	if err == nil {
		y := (1 << z) - 1 - yTms
		n := math.Pow(2, float64(z))
		config.CenterLng = float64(x)/n*360.0 - 180.0
		latRad := math.Atan(math.Sinh(math.Pi * (1 - 2*float64(y)/n)))
		config.CenterLat = latRad * 180.0 / math.Pi
	}
	return config
}

func GetTileData(db *sql.DB, z, x, y int) ([]byte, error) {
	yTms := (1 << z) - 1 - y
	var tileData []byte
	err := db.QueryRow("SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?", z, x, yTms).Scan(&tileData)
	return tileData, err
}
