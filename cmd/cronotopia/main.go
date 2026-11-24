// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cronotopia/acta"
	"cronotopia/atlas"
	"cronotopia/corpus"
	"cronotopia/media"
	"cronotopia/vfs"
	"cronotopia/web"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/shell"
	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	Name    = "Cronotopia"
	Version = "8.11.24"
)

var (
	options *Config
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

func main() {
	options = &Config{}

	flag.StringVar(&options.dbPath, "db", "cronotopia.db", "SQLite database path")
	flag.Var(&options.dbAdd, "db-add", "Additional database to load (repeatable or comma-separated)")
	flag.BoolVar(&options.dbShell, "db-shell", false, "Start interactive SQLite shell")
	flag.BoolVar(&options.compress, "compress", false, "Compress database (-db) to seekable zstd")
	flag.BoolVar(&options.decompress, "decompress", false, "Decompress database (-db) from seekable zstd")
	flag.StringVar(&options.wikidataImport, "import-wikidata", "", "Wikidata dump URL or file (.json, .gz, .bz2)")
	flag.StringVar(&options.wikipediaImport, "import-wikipedia", "", "Wikipedia Enterprise HTML dump URL or tar.gz file")
	flag.StringVar(&options.wikiliteImport, "import-wikilite", "", "Import pre-indexed Wikilite SQLite database")
	flag.StringVar(&options.ggufImport, "import-gguf", "", "Import GGUF model directly into the db")
	flag.StringVar(&options.pmtilesImport, "import-pmtiles", "", "Import PMTiles file into the tiles table")
	flag.StringVar(&options.etopoImport, "import-etopo", "", "Import NOAA ETOPO GeoTIFF into elevations table")
	flag.StringVar(&options.mediaImport, "import-media", "", "Import media SQLite database")
	flag.IntVar(&options.zoom, "zoom", 12, "Max zoom level")

	flag.BoolVar(&options.aiSync, "ai-sync", false, "Generate vector embeddings for imported articles")
	flag.BoolVar(&options.aiAnn, "ai-ann", true, "Generate ANN clustered vector index")
	flag.IntVar(&options.aiAnnSize, "ai-ann-size", 64, "MRL Matryoshka dimensionality")
	flag.BoolVar(&options.aiApi, "ai-api", false, "Use HTTP API for embeddings")
	flag.StringVar(&options.aiApiKey, "ai-api-key", "", "API Key for AI provider")
	flag.StringVar(&options.aiApiUrl, "ai-api-url", "http://localhost:9931/v1/embeddings", "AI API URL")
	flag.StringVar(&options.aiModel, "ai-model", "Qwen3-Embedding-0.6B-Q8_0", "Model identifier")
	flag.StringVar(&options.aiModelPrefixSave, "ai-model-prefix-save", "", "Prefix for passage embeddings")
	flag.StringVar(&options.aiModelPrefixSearch, "ai-model-prefix-search", "Instruct: Retrieve relevant passages\nQuery: ", "Prefix for query embeddings")
	flag.BoolVar(&options.aiCache, "ai-cache", false, "Keep model weights cached in RAM")

	flag.IntVar(&options.threads, "threads", 0, "Number of CPU threads to use (0 = auto)")
	flag.StringVar(&options.webHost, "web-host", "127.0.0.1", "Web API server host")
	flag.IntVar(&options.webPort, "web-port", 35248, "Web API server port")
	flag.BoolVar(&options.log, "log", false, "Enable logging")
	flag.StringVar(&options.logFile, "log-file", "", "Log file output path")

	flag.Usage = func() {
		fmt.Printf("%s v%s\n", Name, Version)
		fmt.Println("Usage: cronotopia [options]")
		fmt.Println("\nOptions:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if options.logFile != "" {
		f, err := os.OpenFile(options.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening log file %q: %v\n", options.logFile, err)
			os.Exit(1)
		}
		log.SetOutput(f)
	} else if !options.log {
		log.SetOutput(io.Discard)
	}

	if options.compress && options.decompress {
		fmt.Fprintln(os.Stderr, "Error: cannot specify both -decompress and -compress")
		os.Exit(1)
	}

	if options.compress {
		if strings.HasSuffix(options.dbPath, ".zst") {
			fmt.Fprintf(os.Stderr, "Error: database %q already ends in .zst\n", options.dbPath)
			os.Exit(1)
		}
		dst := options.dbPath + ".zst"
		if err := vfs.CompressDB(options.dbPath, dst, numWorkers()); err != nil {
			fmt.Fprintf(os.Stderr, "Compress error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if options.decompress {
		if !strings.HasSuffix(options.dbPath, ".zst") {
			fmt.Fprintf(os.Stderr, "Error: cannot decompress %q (expected .zst suffix)\n", options.dbPath)
			os.Exit(1)
		}
		dst := strings.TrimSuffix(options.dbPath, ".zst")
		if err := vfs.DecompressDB(options.dbPath, dst); err != nil {
			fmt.Fprintf(os.Stderr, "Decompress error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	handler, err := openDatabase(options.dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer handler.Close()

	conn := handler.pool.Get(context.Background())
	if conn == nil {
		log.Fatalf("Database pool busy")
	}

	if options.ggufImport != "" {
		if err := corpus.ImportGGUF(handler.pool, options.ggufImport); err != nil {
			log.Fatalf("Model import error: %v", err)
		}
		log.Println("Model imported into database.")
		handler.pool.Put(conn)
		return
	}

	if options.pmtilesImport != "" {
		if err := atlas.ImportPMTiles(conn, options.pmtilesImport, options.zoom); err != nil {
			log.Fatalf("PMTiles import error: %v", err)
		}
		log.Println("PMTiles import completed successfully.")
		handler.pool.Put(conn)
		return
	}

	if options.etopoImport != "" {
		if err := atlas.ImportETOPO(conn, options.etopoImport, numWorkers()); err != nil {
			log.Fatalf("ETOPO import error: %v", err)
		}
		log.Println("ETOPO import completed successfully.")
		handler.pool.Put(conn)
		return
	}

	if options.wikidataImport != "" {
		if err := acta.ImportWikidata(conn, options.wikidataImport, numWorkers()); err != nil {
			log.Fatalf("Wikidata import error: %v", err)
		}
		log.Println("Wikidata import completed successfully.")
		handler.pool.Put(conn)
		return
	}

	if options.wikipediaImport != "" {
		handler.pool.Put(conn)
		if err := corpus.ImportWikipedia(handler.pool, options.wikipediaImport); err != nil {
			log.Fatalf("Wikipedia import error: %v", err)
		}
		log.Println("Wikipedia import completed successfully.")
		return
	}

	if options.wikiliteImport != "" {
		handler.pool.Put(conn)
		if err := corpus.ImportWikilite(handler.pool, options.wikiliteImport); err != nil {
			log.Fatalf("Wikilite import error: %v", err)
		}
		log.Println("Wikilite import completed successfully.")
		return
	}

	if options.mediaImport != "" {
		if err := media.Import(conn, options.mediaImport); err != nil {
			log.Fatalf("Media import error: %v", err)
		}
		log.Println("Media import completed successfully.")
		handler.pool.Put(conn)
		return
	}

	if options.dbShell {
		shell.Run(conn)
		handler.pool.Put(conn)
		return
	}

	corpusCfg := corpus.Config{
		Model:             options.aiModel,
		ModelPrefixSave:   options.aiModelPrefixSave,
		ModelPrefixSearch: options.aiModelPrefixSearch,
		Ann:               options.aiAnn,
		AnnSize:           options.aiAnnSize,
		Api:               options.aiApi,
		ApiKey:            options.aiApiKey,
		ApiUrl:            options.aiApiUrl,
		Cache:             options.aiCache,
		Threads:           numWorkers(),
	}

	aiEngine := corpus.NewEngine(corpusCfg)
	if err := aiEngine.Init(conn); err != nil {
		log.Printf("AI initialization warning: %v", err)
		aiEngine = nil
	}
	handler.pool.Put(conn)

	if options.aiSync && aiEngine != nil {
		if err := aiEngine.ProcessEmbeddings(handler.pool); err != nil {
			log.Fatalf("Embeddings processing error: %v", err)
		}
		log.Println("AI sync completed successfully.")
		return
	}

	server := web.NewServer(handler.pool, aiEngine)
	if err := server.Start(options.webHost, options.webPort); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
