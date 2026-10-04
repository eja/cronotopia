// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	BatchSize     = 10000
	ChannelBuffer = 1000
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

type ExtractedData struct {
	ID    int
	Times []TimeRecord
	Links []LinkRecord
	Place []PlaceRecord
	Query []QueryRecord
}

type TimeRecord struct {
	Code                 int
	Y, M, D, H, Min, Sec int
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
	Lang, Label, Data string
}

type Entity struct {
	ID           string             `json:"id"`
	Claims       map[string][]Claim `json:"claims"`
	Labels       map[string]Term    `json:"labels"`
	Descriptions map[string]Term    `json:"descriptions"`
}

type Claim struct {
	Mainsnak Snak `json:"mainsnak"`
}

type Snak struct {
	Datavalue Datavalue `json:"datavalue"`
}

type Datavalue struct {
	Value json.RawMessage `json:"value"`
}

type TimeValue struct {
	Time string `json:"time"`
}

type CoordinateValue struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Precision float64 `json:"precision"`
}

type EntityIdValue struct {
	ID string `json:"id"`
}

type Term struct {
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

func (h *DBHandler) PostProcessWikidata() error {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	log.Println("Populating R*Tree spatial index...")
	err := sqlitex.Execute(conn, `
		INSERT OR REPLACE INTO entity_places_rtree (id, min_lat, max_lat, min_lon, max_lon)
		SELECT id, latitude, latitude, longitude, longitude FROM entity_places
	`, nil)
	if err != nil {
		log.Printf("Warning updating R*Tree: %v", err)
	}

	log.Println("Optimizing database VACUUM...")
	return sqlitex.Execute(conn, "VACUUM", nil)
}

func runWikidataImport(src string) {
	langs := strings.Split(options.language, ",")
	log.Println("Starting Wikidata import...")

	reader, totalSize, bytesRead, err := openInput(src)
	if err != nil {
		log.Fatalf("Failed to open source %s: %v", src, err)
	}
	defer reader.Close()

	dataChan := make(chan ExtractedData, ChannelBuffer)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		wikidataDBWorker(dataChan)
	}()

	parseWikidataStream(reader, dataChan, langs, totalSize, bytesRead)
	close(dataChan)
	wg.Wait()

	log.Println("Running post-processing...")
	if err := db.PostProcessWikidata(); err != nil {
		log.Printf("Post process warning: %v", err)
	}
	log.Println("Wikidata import finished.")
}

func openInput(src string) (io.ReadCloser, int64, *int64, error) {
	var baseReader io.ReadCloser
	var totalSize int64

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequest("GET", src, nil)
		if err != nil {
			return nil, 0, nil, err
		}

		req.Header.Set("User-Agent", fmt.Sprintf("%s/%s", Name, Version))

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

	var finalReader io.Reader
	ext := strings.ToLower(filepath.Ext(src))
	if ext == ".gz" {
		gz, err := gzip.NewReader(counter)
		if err != nil {
			baseReader.Close()
			return nil, 0, nil, err
		}
		finalReader = gz
	} else if ext == ".bz2" {
		finalReader = bzip2.NewReader(counter)
	} else {
		finalReader = counter
	}

	return &wrappedReadCloser{Reader: finalReader, Closer: baseReader}, totalSize, bytesRead, nil
}

func parseWikidataStream(r io.Reader, out chan<- ExtractedData, langs []string, totalSize int64, bytesRead *int64) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 50*1024*1024)

	count := 0
	start := time.Now()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] == '[' || line[0] == ']' {
			continue
		}
		if line[len(line)-1] == ',' {
			line = line[:len(line)-1]
		}

		var ent Entity
		if err := json.Unmarshal(line, &ent); err != nil {
			continue
		}

		idVal, ok := parseID(ent.ID)
		if !ok {
			continue
		}

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
				var tv TimeValue
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
				var cv CoordinateValue
				if json.Unmarshal(valRaw, &cv) == nil {
					data.Place = append(data.Place, PlaceRecord{pCode, cv.Latitude, cv.Longitude, cv.Precision})
					hasRelevant = true
				}
			}

			if isL {
				var ev EntityIdValue
				if json.Unmarshal(valRaw, &ev) == nil {
					if lVal, ok := parseID(ev.ID); ok {
						data.Links = append(data.Links, LinkRecord{pCode, lVal})
						hasRelevant = true
					}
				}
			}
		}

		if hasRelevant {
			for _, lang := range langs {
				if label, ok := ent.Labels[lang]; ok {
					desc := ""
					if d, ok := ent.Descriptions[lang]; ok {
						desc = d.Value
					}
					data.Query = append(data.Query, QueryRecord{lang, label.Value, desc})
				}
			}
			out <- data
		}

		count++
		if count%50000 == 0 {
			elapsed := time.Since(start)
			pct := 0.0
			if totalSize > 0 && *bytesRead > 0 {
				pct = float64(*bytesRead) / float64(totalSize) * 100
			}
			log.Printf("Progress: %.2f%% | %d entities | %.0f items/sec", pct, count, float64(count)/elapsed.Seconds())
		}
	}
}

func wikidataDBWorker(in <-chan ExtractedData) {
	conn := db.pool.Get(context.Background())
	if conn == nil {
		log.Fatal("failed to get connection")
	}
	defer db.pool.Put(conn)

	_ = sqlitex.Execute(conn, "CREATE TEMPORARY TABLE IF NOT EXISTS link (id INTEGER, code INTEGER, value INTEGER);", nil)

	count := 0
	deferFn := sqlitex.Transaction(conn)
	var err error

	for item := range in {
		for _, t := range item.Times {
			jd := DateToJulianDay(t.Y, t.M, t.D)
			_ = sqlitex.Execute(conn, `INSERT INTO entity_times (entity_id, prop_code, year, month, day, julian_day) VALUES (?,?,?,?,?,?)`, &sqlitex.ExecOptions{
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
			_ = sqlitex.Execute(conn, `INSERT OR REPLACE INTO entity_labels (entity_id, lang, label, description) VALUES (?,?,?,?)`, &sqlitex.ExecOptions{
				Args: []any{item.ID, q.Lang, q.Label, q.Data},
			})
		}

		count++
		if count >= BatchSize {
			deferFn(&err)
			deferFn = sqlitex.Transaction(conn)
			count = 0
		}
	}
	deferFn(&err)

	_ = sqlitex.Execute(conn, `
		INSERT INTO entity_places (entity_id, prop_code, latitude, longitude, precision)
		SELECT link.id, link.code, p.latitude, p.longitude, p.precision 
		FROM link INNER JOIN entity_places p ON link.value = p.entity_id 
		WHERE link.code IN (19, 20)
	`, nil)
}
