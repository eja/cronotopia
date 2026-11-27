// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package vfs

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/SaveTheRbtz/zstd-seekable-format-go/pkg/framecache"
	"github.com/klauspost/compress/zstd"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

var (
	schemaCacheMu sync.RWMutex
	schemaCache   = make(map[string]string)
)

func InvalidateSchemaCache() {
	schemaCacheMu.Lock()
	schemaCache = make(map[string]string)
	schemaCacheMu.Unlock()
}

func TableSchema(conn *sqlite.Conn, table string) string {
	schemaCacheMu.RLock()
	cached, ok := schemaCache[table]
	schemaCacheMu.RUnlock()
	if ok {
		return cached
	}

	schemaCacheMu.Lock()
	defer schemaCacheMu.Unlock()

	if cached, ok := schemaCache[table]; ok {
		return cached
	}

	candidates := []string{"corpus_db", "map_db", "elevation_db", "media_db", "main"}
	var firstFound string

	for _, s := range candidates {
		var exists bool
		_ = sqlitex.Execute(conn, "SELECT 1 FROM pragma_table_list WHERE schema = ? AND name = ? LIMIT 1", &sqlitex.ExecOptions{
			Args: []any{s, table},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				exists = true
				return nil
			},
		})
		if !exists {
			continue
		}

		if firstFound == "" {
			firstFound = s
		}

		var hasRow bool
		_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT 1 FROM %s.%s LIMIT 1", s, table), &sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				hasRow = true
				return nil
			},
		})
		if hasRow {
			schemaCache[table] = s
			return s
		}
	}

	res := "main"
	if firstFound != "" {
		res = firstFound
	}
	schemaCache[table] = res
	return res
}

func IsSQLiteHeader(header []byte) bool {
	return len(header) >= 16 && string(header[:16]) == "SQLite format 3\x00"
}

type AttachedTarget struct {
	Schema         string
	Path           string
	URI            string
	IsZst          bool
	ZstFile        *os.File
	ZstdDec        *zstd.Decoder
	SeekableReader io.Closer
}

func (t *AttachedTarget) Close() error {
	var firstErr error
	if t.SeekableReader != nil {
		if err := t.SeekableReader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if t.ZstdDec != nil {
		t.ZstdDec.Close()
	}
	if t.ZstFile != nil {
		if err := t.ZstFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func PrepareTarget(idx int, filePath string) (*AttachedTarget, error) {
	if filePath == "" {
		return nil, nil
	}

	absPath, err := filepath.Abs(filePath)
	if err == nil {
		filePath = absPath
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database (%s): %w", filePath, err)
	}

	header := make([]byte, 16)
	n, _ := f.ReadAt(header, 0)
	header = header[:n]

	target := &AttachedTarget{
		Path: filePath,
	}

	if IsZstdHeader(header) {
		target.IsZst = true
		target.ZstFile = f
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to initialize zstd decoder (%s): %w", filePath, err)
		}
		target.ZstdDec = dec

		fCache := framecache.NewFIFO(framecache.Limits{MaxFrames: 128})
		sReader, err := seekable.NewReader(f, dec, seekable.WithReaderFrameCache(fCache))
		if err != nil {
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to create seekable reader (%s): %w", filePath, err)
		}
		target.SeekableReader = sReader

		uncompressedSize, err := sReader.Seek(0, io.SeekEnd)
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to seek end (%s): %w", filePath, err)
		}
		if _, err := sReader.Seek(0, io.SeekStart); err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to seek start (%s): %w", filePath, err)
		}

		vfsName, err := GetOrInitVFS()
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to initialize global VFS: %w", err)
		}

		virtualName := fmt.Sprintf("attached_%d.sqlite", idx)
		GlobalRouter.Register(virtualName, NewLockedReaderAt(sReader), uncompressedSize)
		target.URI = fmt.Sprintf("file:%s?vfs=%s&mode=ro&immutable=1", virtualName, vfsName)
	} else if IsSQLiteHeader(header) {
		f.Close()
		cleanPath := filepath.ToSlash(filePath)
		if !strings.HasPrefix(cleanPath, "file:") {
			target.URI = fmt.Sprintf("file:%s?mode=ro&immutable=1", cleanPath)
		} else if !strings.Contains(cleanPath, "immutable=") {
			if strings.Contains(cleanPath, "?") {
				target.URI = cleanPath + "&mode=ro&immutable=1"
			} else {
				target.URI = cleanPath + "?mode=ro&immutable=1"
			}
		} else {
			target.URI = cleanPath
		}
	} else {
		f.Close()
		return nil, fmt.Errorf("unsupported database format (%s)", filePath)
	}

	return target, nil
}

func HasSchemaTable(conn *sqlite.Conn, schema, table string) bool {
	var exists bool
	_ = sqlitex.Execute(conn, "SELECT 1 FROM pragma_table_list WHERE schema = ? AND name = ? LIMIT 1", &sqlitex.ExecOptions{
		Args: []any{schema, table},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			exists = true
			return nil
		},
	})
	return exists
}

func GetSetting(conn *sqlite.Conn, key string) string {
	var schemas []string
	_ = sqlitex.Execute(conn, "SELECT schema FROM pragma_table_list WHERE name = 'settings'", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			schemas = append(schemas, stmt.ColumnText(0))
			return nil
		},
	})
	for _, s := range schemas {
		var val string
		_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT value FROM %s.settings WHERE key = ? LIMIT 1", s), &sqlitex.ExecOptions{
			Args: []any{key},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				val = stmt.ColumnText(0)
				return nil
			},
		})
		if val != "" {
			return val
		}
	}
	return ""
}

type DBHandler struct {
	Pool       *sqlitex.Pool
	IsReadOnly bool
	IsZst      bool
	zstFile    *os.File
	attached   []*AttachedTarget
}

func (h *DBHandler) Close() error {
	var firstErr error
	if h.Pool != nil {
		if err := h.Pool.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, att := range h.attached {
		if err := att.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if h.zstFile != nil {
		_ = h.zstFile.Close()
	}
	return firstErr
}

func detectSchema(uri string) (string, error) {
	conn, err := sqlite.OpenConn(uri, sqlite.OpenReadOnly|sqlite.OpenURI)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	tables := make(map[string]bool)
	err = sqlitex.Execute(conn, "SELECT name FROM sqlite_master WHERE type='table'", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			tables[stmt.ColumnText(0)] = true
			return nil
		},
	})
	if err != nil {
		return "", err
	}

	switch {
	case tables["elevations"]:
		return "elevation_db", nil
	case tables["tiles_shallow"] || tables["tiles_data"] || tables["tiles"]:
		return "map_db", nil
	case tables["media"]:
		return "media_db", nil
	case tables["sections"] || tables["entity_places"] || tables["entity_times"] || tables["entity_label_search"] || tables["tensors"] || tables["vectors"]:
		return "corpus_db", nil
	default:
		return "", fmt.Errorf("unable to autodetect database schema type")
	}
}

func OpenDatabase(dbs []string, readOnly bool) (*DBHandler, error) {
	if len(dbs) == 0 {
		return nil, fmt.Errorf("no database specified")
	}

	dbPath := dbs[0]
	additionalDBs := dbs[1:]

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
	isReadOnly := readOnly

	if f != nil {
		header := make([]byte, 16)
		n, _ := f.ReadAt(header, 0)
		header = header[:n]

		if IsZstdHeader(header) {
			isZst = true
			isReadOnly = true
		} else if IsSQLiteHeader(header) {
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

	if !readOnly && isZst {
		if f != nil && !strings.HasPrefix(dbPath, "/proc/self/fd") {
			f.Close()
		}
		return nil, fmt.Errorf("cannot run modifications or imports on compressed file: %s", dbPath)
	}

	handler := &DBHandler{
		IsReadOnly: isReadOnly,
		IsZst:      isZst,
	}

	seenSchemas := make(map[string]string)
	for i, path := range additionalDBs {
		if path == "" {
			continue
		}
		target, err := PrepareTarget(i, path)
		if err != nil {
			handler.Close()
			return nil, err
		}
		if target != nil {
			schema, err := detectSchema(target.URI)
			if err != nil {
				target.Close()
				handler.Close()
				return nil, fmt.Errorf("failed to detect schema for %s: %w", path, err)
			}
			target.Schema = schema
			if prevPath, exists := seenSchemas[target.Schema]; exists {
				target.Close()
				handler.Close()
				return nil, fmt.Errorf("duplicate database role %q detected for %s (already provided by %s)", target.Schema, target.Path, prevPath)
			}
			seenSchemas[target.Schema] = target.Path
			handler.attached = append(handler.attached, target)
			log.Printf("DB attached: %s (%s, zstd=%v)", target.Schema, target.Path, target.IsZst)
		}
	}

	openPath := dbPath
	if isReadOnly {
		stat, err := f.Stat()
		if err != nil {
			handler.Close()
			return nil, fmt.Errorf("database file stat failed: %w", err)
		}

		vfsName, err := GetOrInitVFS()
		if err != nil {
			handler.Close()
			return nil, fmt.Errorf("failed to register VFS: %w", err)
		}

		if isZst {
			f.Close()
			target, err := PrepareTarget(99999, dbPath)
			if err != nil {
				handler.Close()
				return nil, err
			}
			handler.attached = append(handler.attached, target)
			openPath = target.URI
		} else if strings.HasPrefix(dbPath, "/proc/self/fd") {
			handler.zstFile = f
			GlobalRouter.Register("fd.db", NewLockedReaderAt(f), stat.Size())
			openPath = fmt.Sprintf("file:fd.db?vfs=%s&mode=ro&immutable=1", vfsName)
		} else {
			f.Close()
			cleanPath := filepath.ToSlash(dbPath)
			if len(handler.attached) > 0 {
				openPath = fmt.Sprintf("file:%s?vfs=%s&mode=ro&immutable=1", cleanPath, vfsName)
			} else {
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
		}
	} else {
		if f != nil && !strings.HasPrefix(dbPath, "/proc/self/fd") {
			f.Close()
		}
		initConn, err := sqlite.OpenConn(dbPath, sqlite.OpenReadWrite|sqlite.OpenCreate)
		if err != nil {
			handler.Close()
			return nil, fmt.Errorf("cannot open sqlite database: %w", err)
		}
		_ = sqlitex.ExecuteTransient(initConn, `CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value BLOB) WITHOUT ROWID;`, nil)
		initConn.Close()
	}

	opts := sqlitex.PoolOptions{
		PoolSize: 16,
		PrepareConn: func(conn *sqlite.Conn) error {
			for _, target := range handler.attached {
				if target.Schema == "" {
					continue
				}
				attachSQL := fmt.Sprintf("ATTACH DATABASE '%s' AS %s;", strings.ReplaceAll(target.URI, "'", "''"), target.Schema)
				if err := sqlitex.ExecuteTransient(conn, attachSQL, nil); err != nil {
					return fmt.Errorf("failed to attach database %s: %w", target.Schema, err)
				}
			}
			pragmas := []string{
				"PRAGMA temp_store = MEMORY;",
				"PRAGMA cache_size = -100000;",
			}
			if isReadOnly {
				pragmas = append(pragmas, "PRAGMA query_only = ON;")
			} else {
				pragmas = append(pragmas, "PRAGMA journal_mode = OFF;", "PRAGMA synchronous = OFF;")
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
		handler.Close()
		return nil, fmt.Errorf("failed to open database pool: %w", err)
	}
	handler.Pool = pool

	log.Printf("DB opened: %s (readonly=%v, zstd=%v, attached=%d)", dbPath, isReadOnly, isZst, len(handler.attached))
	return handler, nil
}
