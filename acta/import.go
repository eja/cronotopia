// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package acta

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosnicolaou/pbzip2"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

var timeProps = map[int]bool{
	569: true, 570: true, 571: true, 575: true, 576: true,
	577: true, 580: true, 582: true, 585: true, 729: true,
	730: true, 746: true, 1191: true, 1249: true, 1319: true,
	1326: true, 1619: true, 2031: true, 2032: true, 2669: true,
	2754: true, 3999: true, 5204: true, 6949: true, 7124: true,
	7125: true, 7588: true, 7589: true, 9667: true, 10135: true,
}
var placeProps = map[int]bool{625: true}
var linkProps = map[int]bool{19: true, 20: true}
var propPatterns [][]byte

func init() {
	seen := make(map[int]bool)
	for k := range timeProps {
		seen[k] = true
	}
	for k := range placeProps {
		seen[k] = true
	}
	for k := range linkProps {
		seen[k] = true
	}
	for k := range seen {
		propPatterns = append(propPatterns, []byte(`"`+"P"+strconv.Itoa(k)+`"`))
	}
}

func hasRelevantProperty(line []byte) bool {
	for _, p := range propPatterns {
		if bytes.Contains(line, p) {
			return true
		}
	}
	return false
}

type ExtractedData struct {
	ID    int
	Times []TimeRecord
	Links []LinkRecord
	Place []PlaceRecord
	Query []QueryRecord
}

type TimeRecord struct {
	Code    int
	Y, M, D int
}

type LinkRecord struct {
	Code, Value int
}

type PlaceRecord struct {
	Code      int
	Lat, Lon  float64
	Precision float64
}

type QueryRecord struct {
	Label, Data string
}

type rawEntity struct {
	ID           string                `json:"id"`
	Claims       map[string][]rawClaim `json:"claims"`
	Labels       map[string]rawTerm    `json:"labels"`
	Descriptions map[string]rawTerm    `json:"descriptions"`
}

type rawClaim struct {
	Mainsnak rawSnak `json:"mainsnak"`
}

type rawSnak struct {
	Datavalue rawDatavalue `json:"datavalue"`
}

type rawDatavalue struct {
	Value json.RawMessage `json:"value"`
}

type rawTimeValue struct {
	Time string `json:"time"`
}

type rawCoordinateValue struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Precision float64 `json:"precision"`
	Globe     string  `json:"globe"`
}

type rawEntityIdValue struct {
	ID string `json:"id"`
}

type rawTerm struct {
	Value string `json:"value"`
}

type countingReader struct {
	r     io.Reader
	count *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		*c.count += int64(n)
	}
	return n, err
}

type wrappedReadCloser struct {
	io.Reader
	io.Closer
}

func (w *wrappedReadCloser) Read(p []byte) (int, error) {
	return w.Reader.Read(p)
}

func parseID(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	val, err := strconv.Atoi(s[1:])
	return val, err == nil
}

func InitSchema(conn *sqlite.Conn) error {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS entities (
			id INTEGER PRIMARY KEY,
			article_id INTEGER,
			title TEXT,
			description TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_art ON entities(article_id);`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS entity_label_search USING fts5(
			title,
			content='entities',
			content_rowid='id',
			tokenize='unicode61 remove_diacritics 2'
		);`,

		`CREATE TABLE IF NOT EXISTS entity_times (
			entity_id INTEGER NOT NULL,
			prop_code INTEGER NOT NULL,
			year INTEGER NOT NULL,
			month INTEGER DEFAULT 0,
			day INTEGER DEFAULT 0,
			julian_day INTEGER NOT NULL,
			PRIMARY KEY (entity_id, julian_day, prop_code)
		) WITHOUT ROWID;`,
		`CREATE INDEX IF NOT EXISTS idx_times_jd ON entity_times(julian_day, entity_id);`,

		`CREATE TABLE IF NOT EXISTS entity_places (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_id INTEGER NOT NULL,
			prop_code INTEGER NOT NULL,
			latitude REAL NOT NULL,
			longitude REAL NOT NULL,
			precision REAL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_places_entity ON entity_places(entity_id);`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS entity_places_rtree USING rtree(
			id,
			min_lat, max_lat,
			min_lon, max_lon
		);`,
	}

	for _, s := range sqls {
		if err := sqlitex.ExecuteTransient(conn, s, nil); err != nil {
			return fmt.Errorf("error creating event schema: %w", err)
		}
	}
	return nil
}

func PostProcessWikidata(conn *sqlite.Conn) error {
	var entityCount int
	_ = sqlitex.Execute(conn, "SELECT COUNT(*) FROM entities", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			entityCount = int(stmt.ColumnInt64(0))
			return nil
		},
	})
	if entityCount == 0 {
		return nil
	}

	log.Println("Pruning invalid coordinates and unlabelled entities...")
	_ = sqlitex.Execute(conn, "DELETE FROM entity_places WHERE latitude < -90.0 OR latitude > 90.0 OR longitude < -180.0 OR longitude > 180.0", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM entity_places WHERE entity_id NOT IN (SELECT id FROM entities WHERE title IS NOT NULL AND title != '')", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM entity_times WHERE entity_id NOT IN (SELECT id FROM entities WHERE title IS NOT NULL AND title != '')", nil)

	log.Println("Populating R*Tree spatial index...")
	_ = sqlitex.Execute(conn, "DELETE FROM entity_places_rtree", nil)
	err := sqlitex.Execute(conn, `
		INSERT INTO entity_places_rtree (id, min_lat, max_lat, min_lon, max_lon)
		SELECT id, latitude, latitude, longitude, longitude 
		FROM entity_places
	`, nil)
	if err != nil {
		return fmt.Errorf("failed to populate rtree: %w", err)
	}

	log.Println("Building entity title full-text search index (FTS5)...")
	err = sqlitex.Execute(conn, "INSERT INTO entity_label_search(entity_label_search) VALUES('rebuild')", nil)
	if err != nil {
		return fmt.Errorf("failed to rebuild entity_label_search: %w", err)
	}

	return nil
}

func ImportWikidata(conn *sqlite.Conn, src string, workers int) error {
	log.Println("Starting Wikidata import...")

	if err := InitSchema(conn); err != nil {
		return fmt.Errorf("schema init error: %w", err)
	}

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	reader, totalSize, bytesRead, err := openInput(src, workers)
	if err != nil {
		return fmt.Errorf("failed to open source %s: %w", src, err)
	}
	defer reader.Close()

	dataChan := make(chan ExtractedData, 1000)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		wikidataDBWorker(conn, dataChan)
	}()

	parseWikidataStream(reader, dataChan, totalSize, bytesRead)
	close(dataChan)
	wg.Wait()

	log.Println("Running post-processing...")
	if err := PostProcessWikidata(conn); err != nil {
		log.Printf("Post process warning: %v", err)
	}
	log.Println("Wikidata import finished.")
	return nil
}

func openInput(src string, workers int) (io.ReadCloser, int64, *int64, error) {
	var baseReader io.ReadCloser
	var totalSize int64

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequest("GET", src, nil)
		if err != nil {
			return nil, 0, nil, err
		}
		req.Header.Set("User-Agent", "Cronotopia")
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, 0, nil, fmt.Errorf("http error %d", resp.StatusCode)
		}
		baseReader = resp.Body
		totalSize = resp.ContentLength
	} else {
		f, err := os.Open(src)
		if err != nil {
			return nil, 0, nil, err
		}
		stat, _ := f.Stat()
		if stat != nil {
			totalSize = stat.Size()
		}
		baseReader = f
	}

	bytesRead := new(int64)
	counter := &countingReader{r: baseReader, count: bytesRead}

	ext := strings.ToLower(filepath.Ext(src))
	if ext == ".gz" {
		gz, err := gzip.NewReader(counter)
		if err != nil {
			baseReader.Close()
			return nil, 0, nil, err
		}
		return &wrappedReadCloser{Reader: gz, Closer: baseReader}, totalSize, bytesRead, nil
	} else if ext == ".bz2" {
		bz := pbzip2.NewReader(context.Background(), counter, pbzip2.DecompressionOptions(pbzip2.BZConcurrency(workers)))
		return &wrappedReadCloser{Reader: bz, Closer: baseReader}, totalSize, bytesRead, nil
	}

	return &wrappedReadCloser{Reader: counter, Closer: baseReader}, totalSize, bytesRead, nil
}

func parseWikidataStream(r io.Reader, out chan<- ExtractedData, totalSize int64, bytesRead *int64) {
	reader := bufio.NewReaderSize(r, 4*1024*1024)
	count := 0
	start := time.Now()

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 && line[0] != '[' && line[0] != ']' {
				if line[len(line)-1] == ',' {
					line = line[:len(line)-1]
				}

				if hasRelevantProperty(line) {
					var ent rawEntity
					if json.Unmarshal(line, &ent) == nil {
						if idVal, ok := parseID(ent.ID); ok {
							data := ExtractedData{ID: idVal}
							hasRelevant := false

							for prop, claims := range ent.Claims {
								if len(claims) == 0 {
									continue
								}
								pCode, ok := parseID(prop)
								if !ok {
									continue
								}

								isT := timeProps[pCode]
								isP := placeProps[pCode]
								isL := linkProps[pCode]
								if !isT && !isP && !isL {
									continue
								}

								valRaw := claims[0].Mainsnak.Datavalue.Value
								if len(valRaw) == 0 {
									continue
								}

								if isT {
									var tv rawTimeValue
									if json.Unmarshal(valRaw, &tv) == nil && len(tv.Time) >= 11 {
										tStr := tv.Time
										if tStr[0] == '+' || tStr[0] == '-' {
											y, _ := strconv.Atoi(tStr[1:5])
											m, _ := strconv.Atoi(tStr[6:8])
											d, _ := strconv.Atoi(tStr[9:11])
											if tStr[0] == '-' {
												y = -y
											}
											data.Times = append(data.Times, TimeRecord{Code: pCode, Y: y, M: m, D: d})
											hasRelevant = true
										}
									}
								}

								if isP {
									var cv rawCoordinateValue
									if json.Unmarshal(valRaw, &cv) == nil {
										if cv.Globe == "" || cv.Globe == "http://www.wikidata.org/entity/Q2" {
											data.Place = append(data.Place, PlaceRecord{pCode, cv.Latitude, cv.Longitude, cv.Precision})
											hasRelevant = true
										}
									}
								}

								if isL {
									var ev rawEntityIdValue
									if json.Unmarshal(valRaw, &ev) == nil {
										if lVal, ok := parseID(ev.ID); ok {
											data.Links = append(data.Links, LinkRecord{pCode, lVal})
											hasRelevant = true
										}
									}
								}
							}

							if hasRelevant {
								var label, desc string
								if l, ok := ent.Labels["en"]; ok && strings.TrimSpace(l.Value) != "" {
									label = l.Value
									if d, ok := ent.Descriptions["en"]; ok {
										desc = d.Value
									}
								} else {
									for lang, l := range ent.Labels {
										if val := strings.TrimSpace(l.Value); val != "" {
											label = val
											if d, ok := ent.Descriptions[lang]; ok {
												desc = d.Value
											}
											break
										}
									}
								}

								if label != "" {
									data.Query = append(data.Query, QueryRecord{label, desc})
									out <- data
								}
							}
						}
					}
				}

				count++
				if count%10000 == 0 {
					elapsed := time.Since(start)
					pct := 0.0
					if totalSize > 0 && *bytesRead > 0 {
						pct = float64(*bytesRead) / float64(totalSize) * 100
					}
					log.Printf("Progress: %.2f%% | %d entities | %.0f items/sec", pct, count, float64(count)/elapsed.Seconds())
				}
			}
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("Wikidata stream reader error: %v", err)
			}
			break
		}
	}
}

func wikidataDBWorker(conn *sqlite.Conn, in <-chan ExtractedData) {
	_ = sqlitex.Execute(conn, "CREATE TEMPORARY TABLE IF NOT EXISTS link (id INTEGER, code INTEGER, value INTEGER);", nil)

	count := 0
	deferFn := sqlitex.Transaction(conn)
	var err error

	for item := range in {
		for _, t := range item.Times {
			jd := DateToJulianDay(t.Y, t.M, t.D)
			_ = sqlitex.Execute(conn, `INSERT OR IGNORE INTO entity_times (entity_id, prop_code, year, month, day, julian_day) VALUES (?,?,?,?,?,?)`, &sqlitex.ExecOptions{
				Args: []any{item.ID, t.Code, t.Y, t.M, t.D, jd},
			})
		}
		for _, p := range item.Place {
			_ = sqlitex.Execute(conn, `INSERT INTO entity_places (entity_id, prop_code, latitude, longitude, precision) VALUES (?,?,?,?,?)`, &sqlitex.ExecOptions{
				Args: []any{item.ID, p.Code, p.Lat, p.Lon, p.Precision},
			})
		}
		for _, l := range item.Links {
			_ = sqlitex.Execute(conn, `INSERT INTO link (id, code, value) VALUES (?,?,?)`, &sqlitex.ExecOptions{
				Args: []any{item.ID, l.Code, l.Value},
			})
		}
		for _, q := range item.Query {
			_ = sqlitex.Execute(conn, `
				INSERT INTO entities (id, title, description) VALUES (?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET 
					title = CASE 
						WHEN entities.title IS NOT NULL AND entities.title != '' THEN entities.title 
						ELSE excluded.title 
					END,
					description = excluded.description
			`, &sqlitex.ExecOptions{
				Args: []any{item.ID, q.Label, q.Data},
			})
		}

		count++
		if count >= 10000 {
			deferFn(&err)
			deferFn = sqlitex.Transaction(conn)
			count = 0
		}
	}
	deferFn(&err)

	_ = sqlitex.Execute(conn, `
		INSERT INTO entity_places (entity_id, prop_code, latitude, longitude, precision)
		SELECT DISTINCT link.id, link.code, p.latitude, p.longitude, p.precision 
		FROM link INNER JOIN entity_places p ON link.value = p.entity_id 
		WHERE link.code IN (19, 20)
	`, nil)
	_ = sqlitex.Execute(conn, "DROP TABLE IF EXISTS link", nil)
}
