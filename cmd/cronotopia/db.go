// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cronotopia/vfs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type DBHandler struct {
	pool       *sqlitex.Pool
	isReadOnly bool
	isZst      bool
	zstFile    *os.File
	attached   []*vfs.AttachedTarget
}

func (h *DBHandler) Close() error {
	var firstErr error
	if h.pool != nil {
		if err := h.pool.Close(); err != nil && firstErr == nil {
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

func openDatabase(dbPath string) (*DBHandler, error) {
	isReadOnly := !options.dbShell &&
		!options.aiSync &&
		options.wikipediaImport == "" &&
		options.wikidataImport == "" &&
		options.wikiliteImport == "" &&
		options.ggufImport == "" &&
		options.pmtilesImport == "" &&
		options.mediaImport == "" &&
		options.etopoImport == ""

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

		if vfs.IsZstdHeader(header) {
			isZst = true
			if options.dbShell {
				isReadOnly = true
			}
		} else if vfs.IsSQLiteHeader(header) {
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

	seenSchemas := make(map[string]string)
	for i, path := range options.dbAdd {
		if path == "" {
			continue
		}
		target, err := vfs.PrepareTarget(i, path)
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

		vfsName, err := vfs.GetOrInitVFS()
		if err != nil {
			handler.Close()
			return nil, fmt.Errorf("failed to register VFS: %w", err)
		}

		if isZst {
			f.Close()
			target, err := vfs.PrepareTarget(99999, dbPath)
			if err != nil {
				handler.Close()
				return nil, err
			}
			handler.attached = append(handler.attached, target)
			openPath = target.URI
		} else if strings.HasPrefix(dbPath, "/proc/self/fd") {
			handler.zstFile = f
			vfs.GlobalRouter.Register("fd.db", vfs.NewLockedReaderAt(f), stat.Size())
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
	handler.pool = pool

	log.Printf("DB opened: %s (readonly=%v, zstd=%v, attached=%d)", dbPath, isReadOnly, isZst, len(handler.attached))
	return handler, nil
}
