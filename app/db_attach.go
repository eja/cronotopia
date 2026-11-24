// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/SaveTheRbtz/zstd-seekable-format-go/pkg/framecache"
	"github.com/klauspost/compress/zstd"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type attachedTarget struct {
	schema         string
	path           string
	uri            string
	isZst          bool
	zstFile        *os.File
	zstdDec        *zstd.Decoder
	seekableReader io.Closer
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
	case tables["sections"] || tables["entity_places"] || tables["entity_times"] || tables["entity_label_search"]:
		return "corpus_db", nil
	default:
		return "", fmt.Errorf("unable to autodetect database schema type")
	}
}

func prepareAttachedTarget(idx int, filePath string) (*attachedTarget, error) {
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

	target := &attachedTarget{
		path: filePath,
	}

	if isZstdHeader(header) {
		target.isZst = true
		target.zstFile = f
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to initialize zstd decoder (%s): %w", filePath, err)
		}
		target.zstdDec = dec

		fCache := framecache.NewFIFO(framecache.Limits{
			MaxFrames: 128,
		})

		sReader, err := seekable.NewReader(f, dec, seekable.WithReaderFrameCache(fCache))
		if err != nil {
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to create seekable reader (%s): %w", filePath, err)
		}
		target.seekableReader = sReader

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

		vfsName, err := getOrInitGlobalVFS()
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to initialize global VFS: %w", err)
		}

		virtualName := fmt.Sprintf("joined_%d.sqlite", idx)
		globalVFSRouter.Register(virtualName, &lockedReaderAt{r: sReader}, uncompressedSize)
		target.uri = fmt.Sprintf("file:%s?vfs=%s&mode=ro&immutable=1", virtualName, vfsName)
	} else if isSQLiteHeader(header) {
		f.Close()
		cleanPath := filepath.ToSlash(filePath)
		if !strings.HasPrefix(cleanPath, "file:") {
			target.uri = fmt.Sprintf("file:%s?mode=ro&immutable=1", cleanPath)
		} else if !strings.Contains(cleanPath, "immutable=") {
			if strings.Contains(cleanPath, "?") {
				target.uri = cleanPath + "&mode=ro&immutable=1"
			} else {
				target.uri = cleanPath + "?mode=ro&immutable=1"
			}
		} else {
			target.uri = cleanPath
		}
	} else {
		f.Close()
		return nil, fmt.Errorf("unsupported database format (%s)", filePath)
	}

	schema, err := detectSchema(target.uri)
	if err != nil {
		if target.seekableReader != nil {
			target.seekableReader.Close()
		}
		if target.zstdDec != nil {
			target.zstdDec.Close()
		}
		if target.zstFile != nil {
			target.zstFile.Close()
		}
		return nil, fmt.Errorf("failed to detect schema for %s: %w", filePath, err)
	}
	target.schema = schema

	return target, nil
}

func (h *DBHandler) initAttachedDatabases() error {
	seenSchemas := make(map[string]string)
	for i, path := range options.dbAdd {
		if path == "" {
			continue
		}
		target, err := prepareAttachedTarget(i, path)
		if err != nil {
			return err
		}
		if target != nil {
			if prevPath, exists := seenSchemas[target.schema]; exists {
				return fmt.Errorf("duplicate database role %q detected for %s (already provided by %s)", target.schema, target.path, prevPath)
			}
			seenSchemas[target.schema] = target.path
			h.attached = append(h.attached, target)
			log.Printf("DB attached: %s (%s, zstd=%v)", target.schema, target.path, target.isZst)
		}
	}
	return nil
}

func (h *DBHandler) attachDatabases(conn *sqlite.Conn) error {
	for _, target := range h.attached {
		attachSQL := fmt.Sprintf("ATTACH DATABASE '%s' AS %s;", strings.ReplaceAll(target.uri, "'", "''"), target.schema)
		if err := sqlitex.ExecuteTransient(conn, attachSQL, nil); err != nil {
			return fmt.Errorf("failed to attach database %s: %w", target.schema, err)
		}
	}
	return nil
}

func hasSchemaTable(conn *sqlite.Conn, schema, table string) bool {
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
