# Cronotopia

An offline-first spatio-temporal database engine and map visualization server written in Go.

Cronotopia ingests and indexes data from Wikidata and Wikipedia, stores vector map tiles, and includes a native neural inference engine for vector embeddings. All records, full-text indices, geospatial R*Trees, vector embeddings, ANN index clusters, GGUF model weights, and map tiles are stored in a single SQLite database file.

## Overview

Cronotopia is built to query historical events and geographic data without external services or internet access. It combines historical entity extraction, full-text article sections, and an integrated vector tile server.

The system includes a pure Go execution engine for quantized GGUF embedding models. This enables semantic and hybrid search locally on CPU without any dependencies. In addition to a web frontend and REST API, Cronotopia implements a Model Context Protocol (MCP) server for integration with AI tools and agents.

## Key Features

*   **100% Offline:** Operates with no network dependencies once the database is populated. No external database engines or runtime libraries required.
*   **Single-File & Compressed Storage:** Entities, coordinates, timestamps, article sections (FTS5), spatial R*Trees, vector embeddings, ANN clusters, GGUF weights, and map tiles live in one SQLite file. Supports transparent, zero-extraction read-only querying directly from seekable Zstandard-compressed (`.zst`) files.
*   **Data Ingestion:**
    *   **Wikidata:** Stream ingestion of large dumps (`.json`, `.gz`, `.bz2`) extracting temporal events, geographic coordinates, and entity links.
    *   **Wikipedia:** Ingestion of Wikipedia Enterprise HTML dumps (`tar.gz`) parsed into structured sections with an FTS5 index.
    *   **MBTiles:** Direct import of Mapbox vector tiles (`.mbtiles`) into the internal tile schema.
    *   **GGUF Models:** Direct import of model weights into SQLite for local embedding generation.
    *   **Wikilite:** One-step import of existing [Wikilite](https://github.com/eja/wikilite) SQLite databases.
*   **Embedded AI & Semantic Search:**
    *   Native Go implementation of Qwen3 transformer inference on CPU for zero-dependency query embedding at search time.
    *   Configurable between low-RAM layer-by-layer reading and cached RAM execution.
    *   Approximate Nearest Neighbor (ANN) index generation using Matryoshka Representation Learning (MRL) dimension reduction.
    *   Hybrid retrieval combining BM25 full-text scoring, vector cosine distance, and spatio-temporal filters.
    *   Optional support for external embedding APIs.
*   **Spatio-Temporal Queries:** Fast spatial bounding-box checks using SQLite R*Tree, Haversine distance calculations, and Julian day calendar conversions for historical dates (exact date, before, after).
*   **Tile Server & Web UI:** Serves vector tiles (PBF/MVT) directly to an embedded MapLibre GL frontend.
*   **MCP Server:** Native `/mcp` endpoint exposing tools over JSON-RPC 2.0 (POST) and Server-Sent Events (GET).

## Installation

### 1. Download Binaries
Pre-compiled standalone binaries for Linux, macOS, and Windows are available on the [Releases](https://github.com/eja/cronotopia/releases/latest) page.

### 2. Download Preprocessed Database
To run Cronotopia without processing raw dumps, download a prebuilt database containing historical entities, articles, embeddings, and vector tiles from [Hugging Face](https://huggingface.co/datasets/eja/cronotopia).

### 3. Building from Source
Requires Go 1.22 or later:

```bash
git clone https://github.com/eja/cronotopia.git
cd cronotopia
go build -o cronotopia .
```

## Usage

Cronotopia operates in **import mode**, **compression mode**, or **server mode**:
* When any `--import-*` flag is provided, Cronotopia executes the requested imports and exits upon completion.
* When `--compress` or `--decompress` is provided, Cronotopia performs database compression or inflation and exits.
* When run without import or compression flags, Cronotopia initializes the AI engine, optionally generates vector embeddings for pending sections (if `--ai-sync` is enabled), and starts the web server.

### 1. Data Ingestion

#### Import Map Tiles (MBTiles)
```bash
./cronotopia --db cronotopia.db --import-mbtiles ./planet.mbtiles
```

#### Import Wikidata Dump
```bash
./cronotopia --db cronotopia.db \
  --import-wikidata ./wikidata-latest-all.json.gz
```

#### Import Wikipedia HTML Dump
```bash
./cronotopia --db cronotopia.db \
  --import-wikipedia ./enwiki-enterprise-html.tar.gz
```

#### Import a GGUF Model
```bash
./cronotopia --db cronotopia.db \
  --import-gguf ./qwen3-embedding-0.6b-q8_0.gguf
```

#### Import a Wikilite Database
```bash
./cronotopia --db cronotopia.db --import-wikilite ./wikilite-en.db
```

### 2. Database Compression (`--compress` / `--decompress`)

Cronotopia supports transparent querying of databases compressed with the seekable Zstandard format. You can compress an existing database to save disk space:

```bash
# Compress cronotopia.db -> cronotopia.db.zst
./cronotopia --db cronotopia.db --compress
```

To decompress a `.zst` database back into a standard SQLite file:

```bash
# Decompress cronotopia.db.zst -> cronotopia.db
./cronotopia --db cronotopia.db.zst --decompress
```

> **Note:** Compressed databases (`.zst`) are strictly read-only. Data ingestion and `--ai-sync` require an uncompressed database.

### 3. Server Mode

Start the web server, tile server, REST API, and MCP endpoint:

```bash
# Standard database
./cronotopia --db cronotopia.db --web-host 127.0.0.1 --web-port 35248

# Direct read-only execution on compressed database (zero disk extraction)
./cronotopia --db cronotopia.db.zst --web-host 127.0.0.1 --web-port 35248
```

The MapLibre interface will be available at `http://localhost:35248`.

#### Syncing Embeddings on Startup (`--ai-sync`)

You can generate embeddings for unindexed article sections prior to launching the server.

> **Performance Note:**
> * **Local Pure-Go Inference:** Optimized for runtime query execution during searches (single sentences on CPU with zero dependencies).
> * **Batch Synchronization (`--ai-sync`):** Processing thousands of article passages locally on CPU can take a significant amount of time. For large dumps, using an accelerated external API endpoint (`--ai-api`) is **strongly recommended** for high ingestion throughput.

```bash
# Recommended for batch indexing: Accelerated external embedding endpoint
./cronotopia --db cronotopia.db --ai-sync \
  --ai-api --ai-api-url "http://localhost:8080/v1/embeddings" \
  --ai-model "Qwen3-Embedding-0.6B-Q8_0"

# Pure local CPU indexing (suitable for small datasets or testing)
./cronotopia --db cronotopia.db --ai-sync
```

## Command-Line Options

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--db` | `cronotopia.db` | SQLite database file path (accepts `.db` or `.zst`). |
| `--compress` | `false` | Compress database (`--db`) to seekable zstd (`.zst`). |
| `--decompress` | `false` | Decompress database (`--db`) from seekable zstd (`.zst`). |
| `--web-host` | `127.0.0.1` | Web API server listen host. |
| `--web-port` | `35248` | Web API server listen port. |
| `--import-wikidata` | `""` | Path or URL to Wikidata dump (`.json`, `.gz`, `.bz2`). |
| `--import-wikipedia` | `""` | Path or URL to Wikipedia Enterprise HTML dump (`.tar.gz`). |
| `--import-wikilite` | `""` | Path to a pre-indexed Wikilite SQLite database. |
| `--import-mbtiles` | `""` | Path to an MBTiles file. |
| `--import-gguf` | `""` | Path to a GGUF model file. |
| `--ai-sync` | `false` | Generate vector embeddings for unindexed sections. |
| `--ai-ann` | `true` | Generate ANN clustered vector index during embedding sync. |
| `--ai-ann-size` | `64` | MRL Matryoshka dimensionality for vector indexing. |
| `--ai-cache` | `false` | Cache model weights in memory. |
| `--ai-api` | `false` | Use external HTTP API for embeddings. |
| `--ai-api-url` | `http://localhost:8080/v1/embeddings` | Embeddings API endpoint URL. |
| `--ai-api-key` | `""` | Bearer key for external embeddings API. |
| `--ai-model` | `Qwen3-Embedding-0.6B-Q8_0` | Model identifier string. |
| `--ai-model-prefix-search` | `Instruct: Retrieve relevant passages\nQuery: ` | Prefix for query embeddings. |
| `--ai-model-prefix-save` | `""` | Prefix for passage embeddings. |
| `--log` | `false` | Enable logging to stdout. |
| `--log-file` | `""` | File path for log output. |

## API Reference

### Search: `GET /api` or `GET /api/search`

Queries records by text, semantic vector, location, or time.

**Parameters:**

| Parameter | Type | Description |
| :--- | :--- | :--- |
| `query` | string | Search text. |
| `mode` | string | `hybrid` (default), `lexical` (BM25), or `semantic` (vector similarity). |
| `latitude` | float | Target latitude for spatial search. |
| `longitude` | float | Target longitude for spatial search. |
| `radius` | float | Search radius in kilometers (default: 100). |
| `year` | int | Astronomical year (negative numbers for BCE). |
| `month` | int | Month (1-12). |
| `day` | int | Day (1-31). |
| `range` | string/int | Date matching: `0` (exact), `-1` or `lt` (before), `1` or `gt` (after). |
| `limit` | int | Maximum results returned (default: 10). |

**Examples:**
```bash
# Events near Rome in 44 BCE
curl "http://localhost:35248/api?latitude=41.9028&longitude=12.4964&radius=25&year=-44"

# Hybrid text and geographic search
curl "http://localhost:35248/api?query=Renaissance+artists&latitude=43.7696&longitude=11.2558"

# Vector semantic search
curl "http://localhost:35248/api?query=ancient+naval+warfare&mode=semantic"
```

### Article Retrieval: `GET /api/article`

Retrieves Wikipedia sections, coordinates, and dates for an entity.

**Parameters:**
*   `id`: Entity ID (e.g. `8467` for Q8467) or Wikipedia article ID.
*   `entity_id`: Filter by Wikidata entity ID.
*   `article_id`: Filter by Wikipedia article ID.

```bash
curl "http://localhost:35248/api/article?id=8467"
```

### Map Metadata and Tiles

*   `GET /api/map`: Returns center coordinates and zoom range from database settings.
*   `GET /tiles/{z}/{x}/{y}.pbf`: Serves vector tiles directly from SQLite.

## Model Context Protocol (MCP)

Cronotopia provides an MCP endpoint at `/mcp` supporting JSON-RPC 2.0 over HTTP POST and SSE streams over GET.

### Exposed Tools

1.  **`search_events`**
    *   Locate historical events around given coordinates, radius, and year.
    *   Arguments:
        *   `latitude` (number, required)
        *   `longitude` (number, required)
        *   `radius` (number, optional, default: 50)
        *   `year` (integer, optional)
        *   `limit` (integer, optional, default: 20)
2.  **`search_knowledge`**
    *   Query indexed Wikipedia articles and sections using hybrid, lexical, or semantic search.
    *   Arguments:
        *   `query` (string, required): Search text.
        *   `mode` (string, optional, default: `"hybrid"`): `"hybrid"`, `"lexical"`, or `"semantic"`.
        *   `limit` (integer, optional, default: 10): Maximum results returned.
3.  **`get_article`**
    *   Retrieve full Wikipedia article sections by ID or Wikidata QID integer.
    *   Arguments:
        *   `id` (integer, required)

## Acknowledgments

*   **[Wikidata](https://www.wikidata.org/):** Source for entities, coordinates, dates, and claims.
*   **[Wikipedia](https://www.wikipedia.org/):** Source for encyclopedic text and article structure.
*   **[OpenStreetMap](https://www.openstreetmap.org/):** Geospatial data source for map tiles.
*   **[Qwen Team](https://github.com/QwenLM):** Base embedding model architecture.
*   **[MapLibre](https://maplibre.org/):** Client-side vector map renderer.
*   **[SQLite](https://www.sqlite.org/):** Embedded storage engine.
