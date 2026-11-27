// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"cronotopia/acta"
	"cronotopia/atlas"
	"cronotopia/corpus"
	"cronotopia/media"
	"cronotopia/vfs"
	"cronotopia/web"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/shell"
)

const (
	Name    = "Cronotopia"
	Version = "8.11.27"
)

var (
	options *Config
)

func main() {
	options = &Config{}

	flag.Var(&options.dbPath, "db", "SQLite database path (repeatable or comma-separated)")
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
	flag.StringVar(&options.aiModelPrefixSearch, "ai-model-prefix-search", "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery: ", "Prefix for query embeddings")
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

	if len(options.dbPath) == 0 {
		options.dbPath = stringSlice{"cronotopia.db"}
	}
	primaryDB := options.dbPath[0]

	if options.compress && options.decompress {
		fmt.Fprintln(os.Stderr, "Error: cannot specify both -decompress and -compress")
		os.Exit(1)
	}

	if options.compress {
		if strings.HasSuffix(primaryDB, ".zst") {
			fmt.Fprintf(os.Stderr, "Error: database %q already ends in .zst\n", primaryDB)
			os.Exit(1)
		}
		dst := primaryDB + ".zst"
		if err := vfs.CompressDB(primaryDB, dst, numWorkers()); err != nil {
			fmt.Fprintf(os.Stderr, "Compress error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if options.decompress {
		if !strings.HasSuffix(primaryDB, ".zst") {
			fmt.Fprintf(os.Stderr, "Error: cannot decompress %q (expected .zst suffix)\n", primaryDB)
			os.Exit(1)
		}
		dst := strings.TrimSuffix(primaryDB, ".zst")
		if err := vfs.DecompressDB(primaryDB, dst); err != nil {
			fmt.Fprintf(os.Stderr, "Decompress error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	readOnly := !options.dbShell &&
		!options.aiSync &&
		options.wikipediaImport == "" &&
		options.wikidataImport == "" &&
		options.wikiliteImport == "" &&
		options.ggufImport == "" &&
		options.pmtilesImport == "" &&
		options.mediaImport == "" &&
		options.etopoImport == ""

	handler, err := vfs.OpenDatabase(options.dbPath, readOnly)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer handler.Close()

	withConn := func(fn func(conn *sqlite.Conn) error) error {
		conn := handler.Pool.Get(context.Background())
		if conn == nil {
			return fmt.Errorf("database pool busy")
		}
		defer handler.Pool.Put(conn)
		return fn(conn)
	}

	if options.ggufImport != "" {
		if err := corpus.ImportGGUF(handler.Pool, options.ggufImport); err != nil {
			log.Fatalf("Model import error: %v", err)
		}
		log.Println("Model imported into database.")
		return
	}

	if options.wikipediaImport != "" {
		if err := corpus.ImportWikipedia(handler.Pool, options.wikipediaImport); err != nil {
			log.Fatalf("Wikipedia import error: %v", err)
		}
		log.Println("Wikipedia import completed successfully.")
		return
	}

	if options.wikiliteImport != "" {
		if err := ImportWikilite(handler.Pool, options.wikiliteImport); err != nil {
			log.Fatalf("Wikilite import error: %v", err)
		}
		log.Println("Wikilite import completed successfully.")
		return
	}

	if options.pmtilesImport != "" {
		err := withConn(func(conn *sqlite.Conn) error {
			return atlas.ImportPMTiles(conn, options.pmtilesImport, options.zoom)
		})
		if err != nil {
			log.Fatalf("PMTiles import error: %v", err)
		}
		log.Println("PMTiles import completed successfully.")
		return
	}

	if options.etopoImport != "" {
		err := withConn(func(conn *sqlite.Conn) error {
			return atlas.ImportETOPO(conn, options.etopoImport, options.zoom, numWorkers())
		})
		if err != nil {
			log.Fatalf("ETOPO import error: %v", err)
		}
		log.Println("ETOPO import completed successfully.")
		return
	}

	if options.wikidataImport != "" {
		err := withConn(func(conn *sqlite.Conn) error {
			return acta.ImportWikidata(conn, options.wikidataImport, numWorkers())
		})
		if err != nil {
			log.Fatalf("Wikidata import error: %v", err)
		}
		log.Println("Wikidata import completed successfully.")
		return
	}

	if options.mediaImport != "" {
		err := withConn(func(conn *sqlite.Conn) error {
			return media.Import(conn, options.mediaImport)
		})
		if err != nil {
			log.Fatalf("Media import error: %v", err)
		}
		log.Println("Media import completed successfully.")
		return
	}

	if options.dbShell {
		_ = withConn(func(conn *sqlite.Conn) error {
			shell.Run(conn)
			return nil
		})
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
	initErr := withConn(func(conn *sqlite.Conn) error {
		return aiEngine.Init(conn)
	})
	if initErr != nil {
		log.Printf("AI initialization warning: %v", initErr)
		aiEngine = nil
	}

	if options.aiSync {
		if aiEngine == nil {
			log.Fatalf("AI engine failed to initialize: %v", initErr)
		}
		if err := aiEngine.ProcessEmbeddings(handler.Pool); err != nil {
			log.Fatalf("Embeddings processing error: %v", err)
		}
		log.Println("AI sync completed successfully.")
		return
	}

	server := web.NewServer(handler.Pool, aiEngine, Name, Version)
	if err := server.Start(options.webHost, options.webPort); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
