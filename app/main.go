// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

const (
	Name    = "Cronotopia"
	Version = "8.10.9"
)

var (
	ai      bool
	db      *DBHandler
	options *Config
)

func main() {
	options = &Config{}

	flag.StringVar(&options.dbPath, "db", "cronotopia.db", "SQLite database path")
	flag.BoolVar(&options.compress, "compress", false, "Compress database (-db) to seekable zstd (.zst)")
	flag.BoolVar(&options.decompress, "decompress", false, "Decompress database (-db) from seekable zstd (.zst)")
	flag.StringVar(&options.wikidataImport, "import-wikidata", "", "Wikidata dump URL or file (.json, .gz, .bz2)")
	flag.StringVar(&options.wikipediaImport, "import-wikipedia", "", "Wikipedia Enterprise HTML dump URL or tar.gz file")
	flag.StringVar(&options.wikiliteImport, "import-wikilite", "", "Import pre-indexed Wikilite SQLite database")
	flag.StringVar(&options.ggufImport, "import-gguf", "", "Import GGUF model directly into the db")
	flag.StringVar(&options.mbtilesImport, "import-mbtiles", "", "Import MBTiles file into the tiles table")

	flag.BoolVar(&options.aiSync, "ai-sync", false, "Generate vector embeddings for imported articles")
	flag.BoolVar(&options.aiAnn, "ai-ann", true, "Generate ANN clustered vector index")
	flag.IntVar(&options.aiAnnSize, "ai-ann-size", 64, "MRL Matryoshka dimensionality")
	flag.BoolVar(&options.aiApi, "ai-api", false, "Use HTTP API for embeddings")
	flag.StringVar(&options.aiApiKey, "ai-api-key", "", "API Key for AI provider")
	flag.StringVar(&options.aiApiUrl, "ai-api-url", "http://localhost:8080/v1/embeddings", "AI API URL")
	flag.StringVar(&options.aiModel, "ai-model", "Qwen3-Embedding-0.6B-Q8_0", "Model identifier")
	flag.StringVar(&options.aiModelPrefixSave, "ai-model-prefix-save", "", "Prefix for passage embeddings")
	flag.StringVar(&options.aiModelPrefixSearch, "ai-model-prefix-search", "Instruct: Retrieve relevant passages\nQuery: ", "Prefix for query embeddings")
	flag.BoolVar(&options.aiCache, "ai-cache", false, "Keep model weights cached in RAM")

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
		if err == nil {
			log.SetOutput(f)
		}
	} else if !options.log {
		log.SetOutput(io.Discard)
	}

	if options.compress && options.decompress {
		fmt.Fprintln(os.Stderr, "Error: cannot specify both -decompress and -compress")
		os.Exit(1)
	}

	if options.compress {
		if !options.log && options.logFile == "" {
			log.SetOutput(os.Stderr)
		}
		if strings.HasSuffix(options.dbPath, ".zst") {
			fmt.Fprintf(os.Stderr, "Error: database %q already ends in .zst\n", options.dbPath)
			os.Exit(1)
		}
		dst := options.dbPath + ".zst"
		if err := CompressDB(options.dbPath, dst); err != nil {
			fmt.Fprintf(os.Stderr, "Compress error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if options.decompress {
		if !options.log && options.logFile == "" {
			log.SetOutput(os.Stderr)
		}
		if !strings.HasSuffix(options.dbPath, ".zst") {
			fmt.Fprintf(os.Stderr, "Error: cannot decompress %q (expected .zst suffix)\n", options.dbPath)
			os.Exit(1)
		}
		dst := strings.TrimSuffix(options.dbPath, ".zst")
		if err := DecompressDB(options.dbPath, dst); err != nil {
			fmt.Fprintf(os.Stderr, "Decompress error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var err error
	db, err = NewDBHandler(options.dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	importing := false

	if options.ggufImport != "" {
		importing = true
		if err := ImportGGUFToDB(db, options.ggufImport); err != nil {
			log.Fatalf("Model import error: %v", err)
		}
		log.Println("Model imported into database.")
	}

	if options.mbtilesImport != "" {
		importing = true
		if err := runMBTilesImport(db, options.mbtilesImport); err != nil {
			log.Fatalf("MBTiles import error: %v", err)
		}
		log.Println("MBTiles import completed successfully.")
	}

	if options.wikidataImport != "" {
		importing = true
		runWikidataImport(options.wikidataImport)
	}

	if options.wikipediaImport != "" {
		importing = true
		if err := runWikipediaImport(options.wikipediaImport); err != nil {
			log.Fatalf("Wikipedia import error: %v", err)
		}
	}

	if options.wikiliteImport != "" {
		importing = true
		if err := runWikiliteImport(db, options.wikiliteImport); err != nil {
			log.Fatalf("Wikilite import error: %v", err)
		}
		log.Println("Wikilite import completed successfully.")
	}

	if !importing {
		if err := aiInit(); err != nil {
			log.Printf("AI initialization warning: %v", err)
		} else {
			ai = true
		}

		if options.aiSync && ai {
			if err := db.ProcessEmbeddings(); err != nil {
				log.Fatalf("Embeddings processing error: %v", err)
			}
		}

		runAPIServer()
	}
}
