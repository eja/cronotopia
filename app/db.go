// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
	"modernc.org/sqlite/vfs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type DBHandler struct {
	pool           *sqlitex.Pool
	isReadOnly     bool
	isZst          bool
	zstFile        *os.File
	zstdDec        *zstd.Decoder
	seekableReader io.Closer
	fsHandle       io.Closer
}

type lockedReaderAt struct {
	mu sync.Mutex
	r  io.ReaderAt
}

func (l *lockedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.ReadAt(p, off)
}

type readerFS struct {
	r    io.ReaderAt
	size int64
}

func (rfs *readerFS) Open(name string) (fs.File, error) {
	clean := strings.TrimPrefix(filepath.ToSlash(name), "/")
	if clean != "db" && filepath.Base(clean) != "db" {
		return nil, fs.ErrNotExist
	}
	return &readerFile{
		SectionReader: io.NewSectionReader(rfs.r, 0, rfs.size),
		size:          rfs.size,
	}, nil
}

type readerFile struct {
	*io.SectionReader
	size int64
}

func (f *readerFile) Stat() (fs.FileInfo, error) {
	return &readerFileInfo{size: f.size}, nil
}

func (f *readerFile) Close() error {
	return nil
}

type readerFileInfo struct {
	size int64
}

func (fi *readerFileInfo) Name() string       { return "db" }
func (fi *readerFileInfo) Size() int64        { return fi.size }
func (fi *readerFileInfo) Mode() fs.FileMode  { return 0444 }
func (fi *readerFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *readerFileInfo) IsDir() bool        { return false }
func (fi *readerFileInfo) Sys() any           { return nil }

func isZstdHeader(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	if header[0] == 0x28 && header[1] == 0xb5 && header[2] == 0x2f && header[3] == 0xfd {
		return true
	}
	if header[1] == 0x2a && header[2] == 0x4d && header[3] == 0x18 && (header[0]&0xf0) == 0x50 {
		return true
	}
	return false
}

func isSQLiteHeader(header []byte) bool {
	return len(header) >= 16 && string(header[:16]) == "SQLite format 3\x00"
}

func NewDBHandler(dbPath string) (*DBHandler, error) {
	isReadOnly := !options.aiSync &&
		options.wikipediaImport == "" &&
		options.wikidataImport == "" &&
		options.wikiliteImport == "" &&
		options.ggufImport == "" &&
		options.mbtilesImport == ""

	var f *os.File
	var err error

	if strings.HasPrefix(dbPath, "/proc/self/fd") {
		parts := strings.Split(dbPath, "/")
		if len(parts) > 0 {
			if fdNum, parseErr := strconv.Atoi(parts[len(parts)-1]); parseErr == nil {
				f = os.NewFile(uintptr(fdNum), dbPath)
			}
		}
	}

	if f == nil {
		f, err = os.Open(dbPath)
	}

	var isZst bool

	if f != nil {
		header := make([]byte, 16)
		n, _ := f.ReadAt(header, 0)
		header = header[:n]

		if isZstdHeader(header) {
			isZst = true
		} else if isSQLiteHeader(header) {
			isZst = false
		} else {
			if !strings.HasPrefix(dbPath, "/proc/self/fd") {
				f.Close()
			}
			return nil, fmt.Errorf("unsupported database format (expected zstd or sqlite): %s", dbPath)
		}
	} else {
		if isReadOnly || !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to open database file: %w", err)
		}
		isZst = false
	}

	if !isReadOnly && isZst {
		if f != nil && !strings.HasPrefix(dbPath, "/proc/self/fd") {
			f.Close()
		}
		return nil, fmt.Errorf("cannot run modifications or imports on compressed file: %s", dbPath)
	}

	handler := &DBHandler{
		isReadOnly: isReadOnly,
		isZst:      isZst,
	}

	openPath := dbPath

	if isReadOnly {
		stat, err := f.Stat()
		if err != nil {
			if !strings.HasPrefix(dbPath, "/proc/self/fd") {
				f.Close()
			}
			return nil, fmt.Errorf("database file stat failed: %w", err)
		}

		if isZst {
			_, _ = f.Seek(0, io.SeekStart)
			handler.zstFile = f

			dec, err := zstd.NewReader(nil)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("failed to initialize zstd decoder: %w", err)
			}
			handler.zstdDec = dec

			sReader, err := seekable.NewReader(f, dec)
			if err != nil {
				dec.Close()
				f.Close()
				return nil, fmt.Errorf("failed to create seekable reader: %w", err)
			}
			handler.seekableReader = sReader

			uncompressedSize, err := sReader.Seek(0, io.SeekEnd)
			if err != nil {
				sReader.Close()
				dec.Close()
				f.Close()
				return nil, fmt.Errorf("failed to read compressed file size: %w", err)
			}
			if _, err := sReader.Seek(0, io.SeekStart); err != nil {
				sReader.Close()
				dec.Close()
				f.Close()
				return nil, fmt.Errorf("failed to seek start: %w", err)
			}

			vfsName, fsHandle, err := vfs.New(&readerFS{r: &lockedReaderAt{r: sReader}, size: uncompressedSize})
			if err != nil {
				sReader.Close()
				dec.Close()
				f.Close()
				return nil, fmt.Errorf("failed to register VFS reader: %w", err)
			}
			handler.fsHandle = fsHandle

			openPath = fmt.Sprintf("file:db?vfs=%s&mode=ro&immutable=1", vfsName)
		} else if strings.HasPrefix(dbPath, "/proc/self/fd") {
			handler.zstFile = f

			vfsName, fsHandle, err := vfs.New(&readerFS{r: &lockedReaderAt{r: f}, size: stat.Size()})
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("failed to register VFS reader: %w", err)
			}
			handler.fsHandle = fsHandle

			openPath = fmt.Sprintf("file:db?vfs=%s&mode=ro&immutable=1", vfsName)
		} else {
			f.Close()
			cleanPath := filepath.ToSlash(dbPath)
			if !strings.HasPrefix(cleanPath, "file:") {
				openPath = fmt.Sprintf("file:%s?immutable=1", cleanPath)
			} else if !strings.Contains(cleanPath, "immutable=") {
				if strings.Contains(cleanPath, "?") {
					openPath = cleanPath + "&immutable=1"
				} else {
					openPath = cleanPath + "?immutable=1"
				}
			}
		}
	} else {
		if f != nil && !strings.HasPrefix(dbPath, "/proc/self/fd") {
			f.Close()
		}
		initConn, err := sqlite.OpenConn(dbPath, sqlite.OpenReadWrite|sqlite.OpenCreate)
		if err != nil {
			return nil, fmt.Errorf("cannot open sqlite database for initialization: %w", err)
		}

		sqls := []string{
			"PRAGMA journal_mode = OFF;",
			"PRAGMA synchronous = OFF;",
			"PRAGMA temp_store = MEMORY;",
			"PRAGMA mmap_size = 268435456;",
			"PRAGMA cache_size = -100000;",

			`CREATE TABLE IF NOT EXISTS settings (
				key TEXT PRIMARY KEY,
				value BLOB
			) WITHOUT ROWID;`,

			`CREATE TABLE IF NOT EXISTS entities (
				id INTEGER PRIMARY KEY,
				article_id INTEGER,
				title TEXT,
				description TEXT
			);`,
			`CREATE INDEX IF NOT EXISTS idx_entities_art ON entities(article_id);`,

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

			`CREATE TABLE IF NOT EXISTS sections (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				entity_id INTEGER NOT NULL,
				title TEXT,
				content TEXT,
				pow INTEGER DEFAULT 0
			);`,
			`CREATE INDEX IF NOT EXISTS idx_sections_entity ON sections(entity_id);`,

			`CREATE VIRTUAL TABLE IF NOT EXISTS section_search USING fts5(
				title,
				content,
				content='sections',
				content_rowid='id'
			);`,

			`CREATE VIRTUAL TABLE IF NOT EXISTS entity_label_search USING fts5(
				title,
				content='entities',
				content_rowid='id',
				tokenize='unicode61 remove_diacritics 2'
			);`,

			`CREATE TABLE IF NOT EXISTS vectors (
				id INTEGER PRIMARY KEY,
				embedding BLOB
			);`,

			`CREATE TABLE IF NOT EXISTS vectors_ann_chunks (
				id INTEGER PRIMARY KEY,
				chunk BLOB
			);`,
			`CREATE TABLE IF NOT EXISTS vectors_ann_index (
				vectors_id INTEGER NOT NULL,
				chunk_id INTEGER NOT NULL,
				chunk_position INTEGER NOT NULL,
        PRIMARY KEY (chunk_id, chunk_position)
			) WITHOUT ROWID;`,

			`CREATE TABLE IF NOT EXISTS vectors_ann_centroids (
				id INTEGER PRIMARY KEY,
				centroid BLOB
			);`,
			`CREATE TABLE IF NOT EXISTS vectors_ann_centroid_chunks (
				centroid_id INTEGER NOT NULL,
				chunk_id INTEGER NOT NULL,
				PRIMARY KEY(centroid_id, chunk_id)
			) WITHOUT ROWID;`,

			`CREATE TABLE IF NOT EXISTS tensors (
				layer INTEGER NOT NULL,
				name TEXT NOT NULL,
				dtype INTEGER NOT NULL,
				dims TEXT NOT NULL,
				data BLOB NOT NULL
			);`,

			`CREATE TABLE IF NOT EXISTS tiles (
				zoom_level INTEGER,
				tile_column INTEGER,
				tile_row INTEGER,
				tile_data BLOB,
				PRIMARY KEY(zoom_level, tile_column, tile_row)
			) WITHOUT ROWID;`,
		}

		for _, s := range sqls {
			if err := sqlitex.ExecuteTransient(initConn, s, nil); err != nil {
				initConn.Close()
				return nil, fmt.Errorf("error initializing table: %s: %w", s, err)
			}
		}

		var placesCount, rtreeCount int
		_ = sqlitex.Execute(initConn, "SELECT COUNT(*) FROM entity_places", &sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				placesCount = int(stmt.ColumnInt64(0))
				return nil
			},
		})
		_ = sqlitex.Execute(initConn, "SELECT COUNT(*) FROM entity_places_rtree", &sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				rtreeCount = int(stmt.ColumnInt64(0))
				return nil
			},
		})
		if placesCount > 0 && rtreeCount < placesCount {
			log.Printf("Syncing spatial R*Tree index (%d / %d records)...", rtreeCount, placesCount)
			_ = sqlitex.Execute(initConn, `
				INSERT INTO entity_places_rtree (id, min_lat, max_lat, min_lon, max_lon)
				SELECT id, latitude, latitude, longitude, longitude 
				FROM entity_places 
				WHERE id NOT IN (SELECT id FROM entity_places_rtree)
			`, nil)
		}

		initConn.Close()
	}

	useCustomVFS := isZst || (isReadOnly && strings.HasPrefix(dbPath, "/proc/self/fd"))

	opts := sqlitex.PoolOptions{
		PoolSize: 16,
		PrepareConn: func(conn *sqlite.Conn) error {
			pragmas := []string{
				"PRAGMA temp_store = MEMORY;",
				"PRAGMA cache_size = -100000;",
			}
			if isReadOnly {
				pragmas = append(pragmas, "PRAGMA query_only = ON;")
				if !useCustomVFS {
					pragmas = append(pragmas, "PRAGMA mmap_size = 268435456;")
				}
			} else {
				pragmas = append(pragmas,
					"PRAGMA journal_mode = OFF;",
					"PRAGMA synchronous = OFF;",
					"PRAGMA mmap_size = 268435456;",
				)
			}
			for _, p := range pragmas {
				if err := sqlitex.ExecuteTransient(conn, p, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}

	if isReadOnly {
		opts.Flags = sqlite.OpenReadOnly | sqlite.OpenURI
	} else {
		opts.Flags = sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenURI
	}

	pool, err := sqlitex.NewPool(openPath, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open database pool: %w", err)
	}
	handler.pool = pool

	if model, err := handler.SettingGet("model"); err == nil && model != "" {
		options.aiModel = model
	}
	if annSizeStr, err := handler.SettingGet("annSize"); err == nil && annSizeStr != "" {
		if n, err := strconv.Atoi(annSizeStr); err == nil && n > 0 {
			options.aiAnnSize = n
		}
	}
	if prefixSearch, err := handler.SettingGet("modelPrefixSearch"); err == nil && prefixSearch != "" {
		options.aiModelPrefixSearch = prefixSearch
	}
	if prefixSave, err := handler.SettingGet("modelPrefixSave"); err == nil && prefixSave != "" {
		options.aiModelPrefixSave = prefixSave
	}

	return handler, nil
}

func (h *DBHandler) Close() error {
	var firstErr error
	if h.pool != nil {
		if err := h.pool.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if h.fsHandle != nil {
		if err := h.fsHandle.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if h.seekableReader != nil {
		if err := h.seekableReader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if h.zstdDec != nil {
		h.zstdDec.Close()
	}
	if h.zstFile != nil {
		if err := h.zstFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
