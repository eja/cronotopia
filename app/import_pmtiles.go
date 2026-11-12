// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"zombiezen.com/go/sqlite/sqlitex"
)

type readerAtCloser interface {
	io.ReaderAt
	io.Closer
}

type httpReaderAt struct {
	url    string
	client *http.Client
}

func (h *httpReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.url, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("remote server does not support byte ranges, status: %s", resp.Status)
	}

	return io.ReadFull(resp.Body, p)
}

func (h *httpReaderAt) Close() error {
	h.client.CloseIdleConnections()
	return nil
}

type pmtilesHeader struct {
	rootDirectoryOffset uint64
	rootDirectoryLength uint64
	jsonMetadataOffset  uint64
	jsonMetadataLength  uint64
	leafDirectoryOffset uint64
	leafDirectoryLength uint64
	tileDataOffset      uint64
	tileDataLength      uint64
	numAddressedTiles   uint64
	numTileEntries      uint64
	numTileContents     uint64
	clustered           bool
	internalCompression uint8
	tileCompression     uint8
	tileType            uint8
	minZoom             uint8
	maxZoom             uint8
	minLon              float64
	minLat              float64
	maxLon              float64
	maxLat              float64
	centerZoom          uint8
	centerLon           float64
	centerLat           float64
}

type pmtilesEntry struct {
	tileID    uint64
	offset    uint64
	length    uint32
	runLength uint32
}

func parsePMTilesHeader(b []byte) (*pmtilesHeader, error) {
	if len(b) < 127 {
		return nil, fmt.Errorf("header too short: %d bytes", len(b))
	}
	if string(b[0:7]) != "PMTiles" {
		return nil, fmt.Errorf("invalid magic bytes for PMTiles: %q", string(b[0:7]))
	}
	specVersion := b[7]
	if specVersion != 3 {
		return nil, fmt.Errorf("unsupported PMTiles spec version: %d", specVersion)
	}

	le := binary.LittleEndian
	return &pmtilesHeader{
		rootDirectoryOffset: le.Uint64(b[8:16]),
		rootDirectoryLength: le.Uint64(b[16:24]),
		jsonMetadataOffset:  le.Uint64(b[24:32]),
		jsonMetadataLength:  le.Uint64(b[32:40]),
		leafDirectoryOffset: le.Uint64(b[40:48]),
		leafDirectoryLength: le.Uint64(b[48:56]),
		tileDataOffset:      le.Uint64(b[56:64]),
		tileDataLength:      le.Uint64(b[64:72]),
		numAddressedTiles:   le.Uint64(b[72:80]),
		numTileEntries:      le.Uint64(b[80:88]),
		numTileContents:     le.Uint64(b[88:96]),
		clustered:           b[96] == 1,
		internalCompression: b[97],
		tileCompression:     b[98],
		tileType:            b[99],
		minZoom:             b[100],
		maxZoom:             b[101],
		minLon:              float64(int32(le.Uint32(b[102:106]))) / 10000000.0,
		minLat:              float64(int32(le.Uint32(b[106:110]))) / 10000000.0,
		maxLon:              float64(int32(le.Uint32(b[110:114]))) / 10000000.0,
		maxLat:              float64(int32(le.Uint32(b[114:118]))) / 10000000.0,
		centerZoom:          b[118],
		centerLon:           float64(int32(le.Uint32(b[119:123]))) / 10000000.0,
		centerLat:           float64(int32(le.Uint32(b[123:127]))) / 10000000.0,
	}, nil
}

func deserializeDirectory(data []byte) ([]pmtilesEntry, error) {
	r := bytes.NewReader(data)
	numEntries, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	entries := make([]pmtilesEntry, numEntries)

	var lastID uint64
	for i := uint64(0); i < numEntries; i++ {
		delta, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		lastID += delta
		entries[i].tileID = lastID
	}

	for i := uint64(0); i < numEntries; i++ {
		runLength, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		entries[i].runLength = uint32(runLength)
	}

	for i := uint64(0); i < numEntries; i++ {
		length, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		entries[i].length = uint32(length)
	}

	for i := uint64(0); i < numEntries; i++ {
		v, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		if v == 0 && i > 0 {
			entries[i].offset = entries[i-1].offset + uint64(entries[i-1].length)
		} else {
			entries[i].offset = v - 1
		}
	}

	return entries, nil
}

func readAndDecompressDirectory(r io.ReaderAt, offset, length uint64, compression uint8) ([]pmtilesEntry, error) {
	compressed := make([]byte, length)
	if _, err := r.ReadAt(compressed, int64(offset)); err != nil {
		return nil, err
	}

	var data []byte
	switch compression {
	case 1:
		data = compressed
	case 2:
		zr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		data, err = io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported internal compression: %d", compression)
	}

	return deserializeDirectory(data)
}

func tileIDToZXY(tileID uint64) (uint8, uint32, uint32) {
	var acc uint64
	for z := uint8(0); z < 27; z++ {
		numTiles := uint64(1) << (2 * z)
		if acc+numTiles > tileID {
			n := uint32(1) << z
			var x, y uint32
			t := tileID - acc
			for s := uint32(1); s < n; s *= 2 {
				rx := uint32(1 & (t / 2))
				ry := uint32(1 & (t ^ uint64(rx)))
				if ry == 0 {
					if rx == 1 {
						x = s - 1 - x
						y = s - 1 - y
					}
					x, y = y, x
				}
				x += s * rx
				y += s * ry
				t /= 4
			}
			return z, x, y
		}
		acc += numTiles
	}
	return 0, 0, 0
}

func countCoordinatesInDirs(reader readerAtCloser, header *pmtilesHeader, maxTileID uint64) (int64, error) {
	var count int64

	var scanDir func(entries []pmtilesEntry) error
	scanDir = func(entries []pmtilesEntry) error {
		for _, e := range entries {
			if e.tileID >= maxTileID {
				if header.clustered {
					break
				}
				continue
			}

			if e.runLength == 0 {
				leafOffset := header.leafDirectoryOffset + e.offset
				leafEntries, err := readAndDecompressDirectory(reader, leafOffset, uint64(e.length), header.internalCompression)
				if err != nil {
					return err
				}
				if err := scanDir(leafEntries); err != nil {
					return err
				}
				continue
			}

			if e.tileID+uint64(e.runLength) > maxTileID {
				count += int64(maxTileID - e.tileID)
			} else {
				count += int64(e.runLength)
			}
		}
		return nil
	}

	rootEntries, err := readAndDecompressDirectory(reader, header.rootDirectoryOffset, header.rootDirectoryLength, header.internalCompression)
	if err != nil {
		return 0, err
	}

	if err := scanDir(rootEntries); err != nil {
		return 0, err
	}

	return count, nil
}

func runPMTilesImport(db *DBHandler, srcPMTilesPath string, maxZoom int) (err error) {
	startTime := time.Now()

	var reader readerAtCloser
	if strings.HasPrefix(srcPMTilesPath, "http://") || strings.HasPrefix(srcPMTilesPath, "https://") {
		reader = &httpReaderAt{
			url: srcPMTilesPath,
			client: &http.Client{
				Transport: &http.Transport{
					MaxIdleConns:        100,
					MaxIdleConnsPerHost: 100,
					IdleConnTimeout:     90 * time.Second,
				},
			},
		}
	} else {
		f, err := os.Open(srcPMTilesPath)
		if err != nil {
			return fmt.Errorf("failed to open pmtiles file: %w", err)
		}
		reader = f
	}
	defer reader.Close()

	headerBuf := make([]byte, 127)
	if _, err := reader.ReadAt(headerBuf, 0); err != nil {
		return fmt.Errorf("failed to read header: %w", err)
	}

	header, err := parsePMTilesHeader(headerBuf)
	if err != nil {
		return err
	}

	log.Printf("PMTiles parsed: MinZoom=%d, MaxZoom=%d, Clustered=%v",
		header.minZoom, header.maxZoom, header.clustered)

	conn := db.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection from pool")
	}
	defer db.pool.Put(conn)

	_ = sqlitex.ExecuteTransient(conn, "PRAGMA synchronous = OFF", nil)
	_ = sqlitex.ExecuteTransient(conn, "PRAGMA journal_mode = MEMORY", nil)
	_ = sqlitex.ExecuteTransient(conn, "PRAGMA cache_size = -500000", nil)

	endTx, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer func() {
		if endTx != nil {
			endTx(&err)
		}
	}()

	log.Println("Clearing existing tiles and map metadata...")
	_ = sqlitex.Execute(conn, "DELETE FROM main.tiles_shallow", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM main.tiles_data", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM main.settings WHERE key LIKE 'map_%'", nil)

	setSetting := func(key, val string) {
		_ = sqlitex.Execute(conn, "INSERT OR REPLACE INTO main.settings (key, value) VALUES (?, ?)", &sqlitex.ExecOptions{
			Args: []any{"map_" + key, val},
		})
	}

	var formatStr string
	switch header.tileType {
	case 1:
		formatStr = "pbf"
	case 2:
		formatStr = "png"
	case 3:
		formatStr = "jpg"
	case 4:
		formatStr = "webp"
	case 5:
		formatStr = "avif"
	default:
		formatStr = "pbf"
	}
	setSetting("format", formatStr)

	setSetting("minzoom", fmt.Sprintf("%d", header.minZoom))

	effMaxZoom := int(header.maxZoom)
	if effMaxZoom > maxZoom {
		effMaxZoom = maxZoom
	}
	setSetting("maxzoom", fmt.Sprintf("%d", effMaxZoom))

	setSetting("bounds", fmt.Sprintf("%f,%f,%f,%f", header.minLon, header.minLat, header.maxLon, header.maxLat))
	setSetting("center", fmt.Sprintf("%f,%f,%d", header.centerLon, header.centerLat, header.centerZoom))

	if header.jsonMetadataLength > 0 {
		metaRaw := make([]byte, header.jsonMetadataLength)
		if _, err := reader.ReadAt(metaRaw, int64(header.jsonMetadataOffset)); err == nil {
			var metaJSON []byte
			if header.internalCompression == 2 {
				if zr, err := gzip.NewReader(bytes.NewReader(metaRaw)); err == nil {
					metaJSON, _ = io.ReadAll(zr)
					_ = zr.Close()
				}
			} else {
				metaJSON = metaRaw
			}

			var metaMap map[string]any
			if err := json.Unmarshal(metaJSON, &metaMap); err == nil {
				for k, v := range metaMap {
					var valStr string
					switch val := v.(type) {
					case string:
						valStr = val
					default:
						if b, err := json.Marshal(val); err == nil {
							valStr = string(b)
						} else {
							valStr = fmt.Sprintf("%v", val)
						}
					}
					setSetting(k, valStr)
				}

				if _, hasJSON := metaMap["json"]; !hasJSON {
					if _, hasVectorLayers := metaMap["vector_layers"]; hasVectorLayers {
						setSetting("json", string(metaJSON))
					}
				}
			}
		}
	}

	stmtData := conn.Prep("INSERT OR IGNORE INTO main.tiles_data (tile_data_id, tile_data) VALUES (?, ?);")
	stmtShallow := conn.Prep("INSERT INTO main.tiles_shallow (zoom_level, tile_column, tile_row, tile_data_id) VALUES (?, ?, ?, ?);")

	var maxTileID uint64 = ^uint64(0)
	if maxZoom < 26 {
		maxTileID = ((uint64(1) << (2 * (maxZoom + 1))) - 1) / 3
	}

	var totalTargetCoords int64
	if effMaxZoom >= int(header.maxZoom) && header.numAddressedTiles > 0 {
		totalTargetCoords = int64(header.numAddressedTiles)
	} else {
		log.Println("Pre-calculating total coordinates...")
		if c, err := countCoordinatesInDirs(reader, header, maxTileID); err == nil {
			totalTargetCoords = c
		}
	}

	var (
		totalCoords    int64
		uniqueBlobs    int64
		coordsInBatch  int
		constBatchSize = 100_000
		seenBlobs      = make(map[uint64]struct{})
	)

	var processEntries func(entries []pmtilesEntry) error
	processEntries = func(entries []pmtilesEntry) error {
		for _, e := range entries {
			if e.tileID >= maxTileID {
				if header.clustered {
					break
				}
				continue
			}

			if e.runLength == 0 {
				leafOffset := header.leafDirectoryOffset + e.offset
				leafEntries, err := readAndDecompressDirectory(reader, leafOffset, uint64(e.length), header.internalCompression)
				if err != nil {
					return fmt.Errorf("failed reading leaf directory: %w", err)
				}
				if err := processEntries(leafEntries); err != nil {
					return err
				}
				continue
			}

			dataID := int64(e.offset)

			for i := uint32(0); i < e.runLength; i++ {
				tileID := e.tileID + uint64(i)
				if tileID >= maxTileID {
					break
				}

				z, x, y := tileIDToZXY(tileID)
				if int(z) > maxZoom {
					continue
				}

				if _, seen := seenBlobs[e.offset]; !seen {
					blob := make([]byte, e.length)
					if _, readErr := reader.ReadAt(blob, int64(header.tileDataOffset+e.offset)); readErr != nil {
						return fmt.Errorf("read tile blob error: %w", readErr)
					}

					stmtData.BindInt64(1, dataID)
					stmtData.BindBytes(2, blob)
					if _, stepErr := stmtData.Step(); stepErr != nil {
						return fmt.Errorf("insert tiles_data error: %w", stepErr)
					}
					stmtData.Reset()

					seenBlobs[e.offset] = struct{}{}
					uniqueBlobs++
				}

				tmsY := (uint32(1) << z) - 1 - y

				stmtShallow.BindInt64(1, int64(z))
				stmtShallow.BindInt64(2, int64(x))
				stmtShallow.BindInt64(3, int64(tmsY))
				stmtShallow.BindInt64(4, dataID)
				if _, stepErr := stmtShallow.Step(); stepErr != nil {
					return fmt.Errorf("insert tiles_shallow error: %w", stepErr)
				}
				stmtShallow.Reset()

				totalCoords++
				coordsInBatch++

				if coordsInBatch >= constBatchSize {
					var commitErr error
					endTx(&commitErr)
					if commitErr != nil {
						return fmt.Errorf("commit batch error: %w", commitErr)
					}
					endTx, err = sqlitex.ImmediateTransaction(conn)
					if err != nil {
						return fmt.Errorf("reopen transaction error: %w", err)
					}
					coordsInBatch = 0
					if totalTargetCoords > 0 {
						pct := float64(totalCoords) / float64(totalTargetCoords) * 100.0
						log.Printf("Imported %d/%d coordinates (%.1f%%) — %d unique blobs...",
							totalCoords, totalTargetCoords, pct, uniqueBlobs)
					} else {
						log.Printf("Imported %d coordinates (%d unique blobs)...", totalCoords, uniqueBlobs)
					}
				}
			}
		}
		return nil
	}

	log.Printf("Reading root directory and streaming tiles up to zoom %d...", maxZoom)
	rootEntries, err := readAndDecompressDirectory(reader, header.rootDirectoryOffset, header.rootDirectoryLength, header.internalCompression)
	if err != nil {
		return fmt.Errorf("failed to read root directory: %w", err)
	}

	if err := processEntries(rootEntries); err != nil {
		return err
	}

	var commitErr error
	endTx(&commitErr)
	endTx = nil
	if commitErr != nil {
		return fmt.Errorf("final commit error: %w", commitErr)
	}

	log.Printf("Import complete! Inserted %d coordinates and %d unique blobs in %s",
		totalCoords, uniqueBlobs, time.Since(startTime).Round(time.Second))

	return nil
}
