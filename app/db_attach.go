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
	"modernc.org/sqlite/vfs"
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
	fsHandle       io.Closer
}

func prepareAttachedTarget(schema, filePath string) (*attachedTarget, error) {
	if filePath == "" {
		return nil, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s database (%s): %w", schema, filePath, err)
	}

	header := make([]byte, 16)
	n, _ := f.ReadAt(header, 0)
	header = header[:n]

	target := &attachedTarget{
		schema: schema,
		path:   filePath,
	}

	if isZstdHeader(header) {
		target.isZst = true
		target.zstFile = f
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to initialize zstd decoder for %s: %w", schema, err)
		}
		target.zstdDec = dec

		fCache := framecache.NewFIFO(framecache.Limits{
			MaxFrames: 128,
		})

		sReader, err := seekable.NewReader(f, dec, seekable.WithReaderFrameCache(fCache))
		if err != nil {
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to create seekable reader for %s: %w", schema, err)
		}
		target.seekableReader = sReader

		uncompressedSize, err := sReader.Seek(0, io.SeekEnd)
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to seek end of %s: %w", schema, err)
		}
		if _, err := sReader.Seek(0, io.SeekStart); err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to seek start of %s: %w", schema, err)
		}

		vfsName, fsHandle, err := vfs.New(&readerFS{r: &lockedReaderAt{r: sReader}, size: uncompressedSize})
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to register VFS for %s: %w", schema, err)
		}
		target.fsHandle = fsHandle
		target.uri = fmt.Sprintf("file:db?vfs=%s&mode=ro&immutable=1", vfsName)
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
		return nil, fmt.Errorf("unsupported database format for %s (%s)", schema, filePath)
	}

	return target, nil
}

func (h *DBHandler) initAttachedDatabases() error {
	pairs := []struct {
		schema string
		path   string
	}{
		{"corpus_db", options.dbCorpus},
		{"ai_db", options.dbAi},
		{"map_db", options.dbMap},
		{"elevation_db", options.dbElevation},
		{"media_db", options.dbMedia},
	}

	for _, p := range pairs {
		if p.path == "" {
			continue
		}
		target, err := prepareAttachedTarget(p.schema, p.path)
		if err != nil {
			return err
		}
		if target != nil {
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

	var settingsBranches []string
	settingsBranches = append(settingsBranches, "SELECT key, value, 1 AS priority FROM main.settings")
	prio := 2
	for _, target := range h.attached {
		if hasSchemaTable(conn, target.schema, "settings") {
			settingsBranches = append(settingsBranches, fmt.Sprintf("SELECT key, value, %d AS priority FROM %s.settings", prio, target.schema))
			prio++
		}
	}

	if len(settingsBranches) > 1 {
		viewSQL := fmt.Sprintf(`CREATE TEMP VIEW IF NOT EXISTS settings AS
			SELECT key, value FROM (
				SELECT key, value, MIN(priority) FROM (
					%s
				) GROUP BY key
			);`, strings.Join(settingsBranches, " UNION ALL "))
		if err := sqlitex.ExecuteTransient(conn, viewSQL, nil); err != nil {
			return fmt.Errorf("failed to create unified settings view: %w", err)
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
