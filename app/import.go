package main

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"database/sql"
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
)

func runImport(importSrc, dbFile, langStr string, logging bool) {
	targetLangs := strings.Split(langStr, ",")

	if logging {
		log.Println("Initializing Database...")
	}
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		PRAGMA journal_mode = MEMORY;
		PRAGMA synchronous = OFF;
		PRAGMA locking_mode = EXCLUSIVE;
		PRAGMA temp_store = MEMORY; 
		PRAGMA cache_size = -200000;
	`)
	if err != nil {
		log.Fatal(err)
	}

	createTables(db)

	reader, totalSize, bytesRead, err := openInput(importSrc, logging)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	dataChan := make(chan ExtractedData, ChannelBuffer)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		dbWorker(db, dataChan)
	}()

	parseStream(reader, dataChan, targetLangs, logging, totalSize, bytesRead)
	close(dataChan)

	wg.Wait()

	if logging {
		log.Println("Running post-processing (joining locations)...")
	}
	postProcess(db)

	if logging {
		log.Println("Import Done.")
	}
}

func openInput(src string, logging bool) (io.ReadCloser, int64, *int64, error) {
	var baseReader io.ReadCloser
	var totalSize int64

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		if logging {
			log.Printf("Downloading from %s...", src)
		}
		resp, err := http.Get(src)
		if err != nil {
			return nil, 0, nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, 0, nil, fmt.Errorf("http status %d", resp.StatusCode)
		}
		baseReader = resp.Body
		totalSize = resp.ContentLength
	} else {
		if logging {
			log.Printf("Reading local file %s...", src)
		}
		f, err := os.Open(src)
		if err != nil {
			return nil, 0, nil, err
		}
		stat, err := f.Stat()
		if err == nil {
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
		bz := bzip2.NewReader(counter)
		finalReader = bz
	} else {
		finalReader = counter
	}

	return &wrappedReadCloser{Reader: finalReader, Closer: baseReader}, totalSize, bytesRead, nil
}

func parseStream(r io.Reader, out chan<- ExtractedData, langs []string, logging bool, totalSize int64, bytesRead *int64) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 50*1024*1024)

	count := 0
	start := time.Now()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if line[0] == '[' || line[0] == ']' {
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
		hasRelevantData := false

		for prop, claims := range ent.Claims {
			if len(claims) == 0 {
				continue
			}

			pCode, ok := parseID(prop)
			if !ok {
				continue
			}

			isTime := timeProps[pCode]
			isPlace := placeProps[pCode]
			isLink := linkProps[pCode]

			if !isTime && !isPlace && !isLink {
				continue
			}

			valRaw := claims[0].Mainsnak.Datavalue.Value
			if len(valRaw) == 0 {
				continue
			}

			if isTime {
				var tv TimeValue
				if json.Unmarshal(valRaw, &tv) == nil && len(tv.Time) >= 11 {
					tStr := tv.Time
					if tStr[0] == '+' || tStr[0] == '-' {
						startIdx := 1
						if len(tStr) > 10 {
							y, _ := strconv.Atoi(tStr[startIdx : startIdx+4])
							m, _ := strconv.Atoi(tStr[startIdx+5 : startIdx+7])
							d, _ := strconv.Atoi(tStr[startIdx+8 : startIdx+10])

							h, min, s := 0, 0, 0
							if len(tStr) >= 19 {
								h, _ = strconv.Atoi(tStr[startIdx+11 : startIdx+13])
								min, _ = strconv.Atoi(tStr[startIdx+14 : startIdx+16])
								s, _ = strconv.Atoi(tStr[startIdx+17 : startIdx+19])
							}

							if tStr[0] == '-' {
								y = -y
							}
							data.Times = append(data.Times, TimeRecord{pCode, y, m, d, h, min, s})
							hasRelevantData = true
						}
					}
				}
			}

			if isPlace {
				var cv CoordinateValue
				if json.Unmarshal(valRaw, &cv) == nil {
					data.Place = append(data.Place, PlaceRecord{pCode, cv.Latitude, cv.Longitude, cv.Precision})
					hasRelevantData = true
				}
			}

			if isLink {
				var ev EntityIdValue
				if json.Unmarshal(valRaw, &ev) == nil {
					if lVal, ok := parseID(ev.ID); ok {
						data.Links = append(data.Links, LinkRecord{pCode, lVal})
						hasRelevantData = true
					}
				}
			}
		}

		if hasRelevantData {
			for _, lang := range langs {
				if label, ok := ent.Labels[lang]; ok {
					descStr := ""
					if desc, ok := ent.Descriptions[lang]; ok {
						descStr = desc.Value
					}
					data.Query = append(data.Query, QueryRecord{lang, label.Value, descStr})
				}
			}
			out <- data
		}

		count++
		if logging && count%10000 == 0 {
			elapsed := time.Since(start)
			pct := 0.0
			etaStr := "N/A"

			currentBytes := *bytesRead
			if totalSize > 0 && currentBytes > 0 {
				pct = float64(currentBytes) / float64(totalSize) * 100
				bytesPerSec := float64(currentBytes) / elapsed.Seconds()
				remainingBytes := float64(totalSize) - float64(currentBytes)
				if bytesPerSec > 0 {
					etaSeconds := remainingBytes / bytesPerSec
					etaStr = formatDuration(time.Duration(etaSeconds) * time.Second)
				}
			}
			itemSpeed := float64(count) / elapsed.Seconds()
			log.Printf("Progress: %.2f%% | Time: %s | ETA: %s | Processed: %d | Speed: %.0f items/sec",
				pct, formatDuration(elapsed), etaStr, count, itemSpeed)
		}
	}
}
