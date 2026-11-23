# Cronotopia

An offline-first spatio-temporal database engine, media server, and interactive map visualization platform written in Go.

Cronotopia ingests and indexes data from Wikidata and Wikipedia, manages books and interactive musical scores, stores vector map tiles and elevation models, and includes a native neural inference engine for quantized GGUF vector embeddings. All records, full-text indices, geospatial R*Trees, vector embeddings, ANN index clusters, GGUF model weights, vector tiles, elevation rasters, and media objects can reside in a single monolithic SQLite database or across modular, attached databases.

## Overview

Cronotopia is engineered to query historical events, encyclopedic knowledge, literature, musical scores, and geographic data without internet access or external runtime dependencies. It combines historical entity extraction, full-text article search, digital media delivery, 3D terrain elevation, and an integrated vector tile server into a unified binary.

The system includes a pure Go execution engine for quantized embedding models, enabling semantic and hybrid search locally on CPU without external libraries. In addition to a responsive web interface and REST API, Cronotopia implements a Model Context Protocol (MCP) server for deep integration with AI tools and autonomous agents.

## Key Features

*   **100% Offline & Zero-Dependency:** Operates without network access once databases are populated. Self-contained Go binary with embedded web assets.
*   **Flexible Storage Architecture (Monolithic or Modular):**
    *   **Single-File Operation:** All tables (corpus, entities, vector embeddings, GGUF tensors, map tiles, elevation raster tiles, media) can live in a single `.db` file.
    *   **Modular Attached Databases:** Attach external specialized databases at startup (`--db-corpus`, `--db-ai`, `--db-map`, `--db-elevation`, `--db-media`) via unified SQLite schemas.
    *   **Transparent Zstandard (`.zst`) Compression:** Transparent, zero-extraction, random-access read-only querying directly from seekable compressed (`.zst`) database files for the main database or any attached database.
*   **Media Delivery & Visualization:**
    *   **Books & Texts:** Integrated paginated reader supporting chunked retrieval of long texts.
    *   **Interactive Musical Scores:** Native playback and notation rendering of MusicXML scores via [alphaTab](https://alphatab.net/) with an embedded SoundFont synthesizer.
    *   **HTTP Range Requests:** The media API supports `Range: bytes=` headers for efficient streaming and offset-based access.
*   **Data Ingestion Pipelines:**
    *   **Wikidata:** Parallel streaming ingestion of large Wikidata JSON dumps extracting temporal events, coordinates, and entity links.
    *   **Wikipedia:** Ingestion of Wikipedia Enterprise HTML dumps into structured sections indexed with SQLite FTS5.
    *   **PMTiles:** Ingestion of vector map tiles up to configurable zoom levels.
    *   **NOAA ETOPO:** Ingestion of ETOPO GeoTIFF elevation rasters into Terrarium-encoded PNG elevation tiles for 3D terrain and dynamic contour generation.
    *   **Media:** Ingestion of external media databases (books, scores).
    *   **GGUF Models:** Direct extraction and ingestion of quantized transformer weights into SQLite BLOBs.
    *   **Wikilite:** One-step import from pre-indexed [Wikilite](https://github.com/eja/wikilite) databases.
*   **Embedded AI & Hybrid Search:**
    *   Native pure-Go transformer inference on CPU for query embeddings at search time.
    *   Low-RAM layer-by-layer reading directly from SQLite BLOBs or full memory caching.
    *   Approximate Nearest Neighbor (ANN) index clustering with Matryoshka Representation Learning (MRL) dimension reduction.
    *   Hybrid retrieval combining BM25 full-text scoring, vector cosine distance, and spatio-temporal filters via Reciprocal Rank Fusion (RRF).
    *   Optional external HTTP embedding API integration.
*   **Spatio-Temporal Indexing:** SQLite R*Trees for geographic bounding-box queries, Haversine distance ranking, and Julian day calendar conversions supporting astronomical years (exact, before, after).
*   **Vector Tile Server & 3D Web Map:**
    *   Serves vector tiles and Terrarium elevation tiles.
    *   Embedded MapLibre GL frontend with 3D terrain, hillshading, contours, cluster spiderfication, and layer toggles (3D elevation, contours, buildings, roads, labels).
    *   Direct extraction of vector feature geometries, centroids, and bounding boxes via map APIs.
*   **Interactive Database Shell:** Built-in interactive SQLite shell with attached database support.
*   **Model Context Protocol (MCP):** Native `/mcp` endpoint exposing historical, semantic, media, and vector map tools over JSON-RPC 2.0 (POST) and Server-Sent Events (GET).

## Installation

### 1. Download Pre-compiled Binaries
Pre-compiled standalone binaries for Linux, macOS, and Windows are available on the [Releases](https://github.com/eja/cronotopia/releases/latest) page.

### 2. Download Preprocessed Datasets
Download prebuilt databases containing historical entities, Wikipedia articles, embeddings, media, and vector tiles from [Hugging Face](https://huggingface.co/datasets/eja/cronotopia).

### 3. Building from Source

```bash
git clone https://github.com/eja/cronotopia.git
cd cronotopia
go build -o cronotopia .
```

## Usage

Cronotopia operates in one of four modes:
1.  **Import Mode:** Triggered by any `--import-*` flag. Runs the ingestion pipeline and exits.
2.  **Compression Mode:** Triggered by `--compress` or `--decompress`. Converts databases to/from seekable Zstandard format and exits.
3.  **AI Sync Mode:** Triggered by `--ai-sync`. Generates vector embeddings and builds the ANN index for pending sections, then exits.
4.  **Interactive Shell Mode:** Triggered by `--db-shell`. Launches an interactive SQLite terminal.
5.  **Server Mode:** Default mode. Initializes the AI engine and starts the web interface, tile server, media server, REST API, and MCP endpoint.

---

### 1. Data Ingestion

#### Import Map Tiles (PMTiles)
```bash
./cronotopia --db cronotopia.db --import-pmtiles ./planet.pmtiles --zoom 12
```

#### Import ETOPO Elevation Data
```bash
./cronotopia --db cronotopia.db --import-etopo ./ETOPO_2022_v1_30s_N90W180_surface.tif
```

#### Import Wikidata Dump
```bash
./cronotopia --db cronotopia.db --import-wikidata ./wikidata-latest-all.json.gz --threads 8
```

#### Import Wikipedia HTML Dump
```bash
./cronotopia --db cronotopia.db --import-wikipedia ./enwiki-enterprise-html.tar.gz
```

#### Import Media (Books, Scores)
```bash
./cronotopia --db cronotopia.db --import-media ./media.db
```

#### Import a GGUF Embedding Model
```bash
./cronotopia --db cronotopia.db --import-gguf ./qwen3-embedding-0.6b-q8_0.gguf
```

#### Import a Wikilite Database
```bash
./cronotopia --db cronotopia.db --import-wikilite ./wikilite-en.db
```

---

### 2. Database Compression (`.zst`)

Cronotopia supports transparent, zero-extraction reading from seekable Zstandard compressed databases:

```bash
# Compress cronotopia.db -> cronotopia.db.zst
./cronotopia --db cronotopia.db --compress

# Decompress cronotopia.db.zst -> cronotopia.db
./cronotopia --db cronotopia.db.zst --decompress
```

> **Note:** Compressed databases (`.zst`) are strictly read-only. Data ingestion and `--ai-sync` require an uncompressed database.

---

### 3. Embedding Synchronization (`--ai-sync`)

To generate embeddings and compute the ANN index for imported sections:

```bash
# Using local pure-Go inference
./cronotopia --db cronotopia.db --ai-sync --ai-ann

# Using an accelerated external OpenAI-compatible embedding API (recommended for large datasets)
./cronotopia --db cronotopia.db --ai-sync \
  --ai-api --ai-api-url "http://localhost:8080/v1/embeddings" \
  --ai-model "Qwen3-Embedding-0.6B-Q8_0"
```

---

### 4. Interactive SQLite Shell

Inspect the database and all attached modular schemas using the embedded SQLite shell:

```bash
./cronotopia --db cronotopia.db --db-shell
```

---

### 5. Running the Server

#### Monolithic Database
```bash
# Standard SQLite file
./cronotopia --db cronotopia.db --web-host 127.0.0.1 --web-port 35248

# Direct read-only execution on compressed database
./cronotopia --db cronotopia.db.zst --web-host 127.0.0.1 --web-port 35248
```

#### Modular Attached Databases
You can keep databases separated and attach them simultaneously at startup:
```bash
./cronotopia \
  --db app.db \
  --db-corpus /data/corpus.db.zst \
  --db-ai /data/qwen3-ai.db \
  --db-map /data/map-tiles.db.zst \
  --db-elevation /data/elevations.db.zst \
  --db-media /data/media.db \
  --web-host 0.0.0.0 --web-port 35248
```

The web UI will be available at `http://localhost:35248`.

---

## Command-Line Options

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--db` | `cronotopia.db` | Primary SQLite database path (supports `.db` or `.zst`). |
| `--db-ai` | `""` | Path to attached AI model database (`ai_db`). |
| `--db-map` | `""` | Path to attached map tiles database (`map_db`). |
| `--db-media` | `""` | Path to attached media database (`media_db`). |
| `--db-corpus` | `""` | Path to attached corpus database (`corpus_db`). |
| `--db-elevation` | `""` | Path to attached elevation tiles database (`elevation_db`). |
| `--db-shell` | `false` | Start interactive SQLite shell and exit. |
| `--compress` | `false` | Compress `--db` to seekable Zstandard (`.zst`). |
| `--decompress` | `false` | Decompress `--db` from seekable Zstandard (`.zst`). |
| `--threads` | `0` | Number of worker threads (0 = number of CPU cores). |
| `--zoom` | `12` | Max zoom level for PMTiles import. |
| `--web-host` | `127.0.0.1` | Web and API server listen host. |
| `--web-port` | `35248` | Web and API server listen port. |
| `--import-wikidata` | `""` | Path or URL to Wikidata dump (`.json`, `.gz`, `.bz2`). |
| `--import-wikipedia` | `""` | Path or URL to Wikipedia Enterprise HTML dump (`.tar.gz`). |
| `--import-wikilite` | `""` | Path to pre-indexed Wikilite SQLite database. |
| `--import-pmtiles` | `""` | Path or HTTP URL to a PMTiles file. |
| `--import-etopo` | `""` | Path to NOAA ETOPO GeoTIFF raster. |
| `--import-media` | `""` | Path to media SQLite database to import. |
| `--import-gguf` | `""` | Path to a GGUF model file. |
| `--ai-sync` | `false` | Generate vector embeddings and ANN index, then exit. |
| `--ai-ann` | `true` | Generate ANN clustered index during embedding sync. |
| `--ai-ann-size` | `64` | MRL Matryoshka dimension for ANN clustering. |
| `--ai-cache` | `false` | Cache GGUF model weights in memory instead of reading per-layer. |
| `--ai-api` | `false` | Use external HTTP API for embeddings. |
| `--ai-api-url` | `http://localhost:9931/v1/embeddings` | Embeddings API endpoint URL. |
| `--ai-api-key` | `""` | API key for external embedding provider. |
| `--ai-model` | `Qwen3-Embedding-0.6B-Q8_0` | Model identifier string. |
| `--ai-model-prefix-search`| `Instruct: Retrieve relevant passages\nQuery: ` | Prefix for query embeddings. |
| `--ai-model-prefix-save`  | `""` | Prefix for stored passage embeddings. |
| `--log` | `false` | Enable application logging. |
| `--log-file` | `""` | Write application log output to a file. |

---

## REST API Reference

### Unified Search: `/api/search`

Searches across historical events, entity labels, Wikipedia sections, and media items.

**Query Parameters:**

| Parameter | Type | Description |
| :--- | :--- | :--- |
| `query` | string | Text search query. |
| `mode` | string | Search mode: `hybrid` (default: RRF blend), `entity` (title/label FTS), `lexical` (section BM25), or `semantic` (vector similarity). |
| `latitude` | float | Center latitude for geographic restriction. |
| `longitude` | float | Center longitude for geographic restriction. |
| `radius` | float | Search radius in kilometers (default: `100.0`). |
| `year` | int | Astronomical year (e.g. `-44` for 44 BCE). |
| `month` | int | Month filter (1–12). |
| `day` | int | Day filter (1–31). |
| `range` | string/int | Date range: `0` (exact), `-1`, `lt`, `lte`, `<` (before), `1`, `gt`, `gte`, `>` (after). |
| `limit` | int | Maximum results to return (default: `10`). |

**Result Object `type` Codes:**
*   `E`: Pure temporal/spatial event match (from Wikidata).
*   `T`: Entity title/label match.
*   `L`: Lexical full-text match in article body (BM25).
*   `S`: Semantic embedding match.
*   `H`: Hybrid multi-signal match (RRF fusion).

**Example:**
```bash
# Search events around Alexandria in 48 BCE
curl "http://localhost:35248/api?latitude=31.2001&longitude=29.9187&radius=50&year=-48"

# Hybrid text and geographic search
curl "http://localhost:35248/api?query=Archimedes&latitude=37.0755&longitude=15.2866"
```

---

### Article Retrieval: `GET /api/article`

Retrieves full structured sections, coordinates, timestamps, and attached media for an entity or article.

**Query Parameters:**
*   `id`: Wikidata QID integer (e.g. `8467` for Q8467) or Wikipedia article ID.
*   `entity_id`: Filter specifically by Wikidata entity ID.
*   `article_id`: Filter specifically by Wikipedia article ID.

```bash
curl "http://localhost:35248/api/article?id=8467"
```

---

### Media Streaming: `GET /api/media`

Streams books or sheet music by media ID, supporting HTTP Range requests for fast pagination and media scrubbing.

**Query Parameters:**
*   `id`: Media row ID (required).
*   `offset`: Starting byte offset (optional).
*   `length`: Number of bytes to read (optional).

**Headers Supported:**
*   `Range: bytes={start}-{end}`
*   Returns `Accept-Ranges: bytes` and `Content-Range: bytes {start}-{end}/{total}`.

**Example:**
```bash
# Fetch the first 50,000 bytes of a book
curl -i "http://localhost:35248/api/media?id=42&offset=0&length=50000"
```

---

### Map Metadata, Tiles & Feature Extraction

#### 1. Map Metadata: `GET /api/map`
Returns center coordinates, default zoom, and bounds from the database configuration:
```bash
curl "http://localhost:35248/api/map"
```

#### 2. Vector Tile: `GET /tiles/{z}/{x}/{y}.pbf`
Serves vector tiles directly from the `tiles` schema. Returns gzip-compressed Protocol Buffers (`application/x-protobuf`).
```bash
curl "http://localhost:35248/tiles/14/8711/5684.pbf"
```

#### 3. Elevation Raster Tile: `GET /tiles/elevations/{z}/{x}/{y}.png`
Serves Terrarium-encoded PNG elevation tiles for 3D terrain rendering and dynamic client-side contour generation:
```bash
curl "http://localhost:35248/tiles/elevations/4/8/5.png"
```

#### 4. Extract Map Centroids: `GET /api/map?latitude={lat}&longitude={lon}`
Extracts vector features from intersecting map tiles within a given radius, returning centroids, bounding boxes, layers, and properties:
```bash
curl "http://localhost:35248/api/map?latitude=41.8902&longitude=12.4922&radius=500"
```

#### 5. Extract Map Feature by ID: `GET /api/map?id={id}&latitude={lat}&longitude={lon}`
Decodes and returns the full geometry coordinates and attributes of a specific vector feature:
```bash
curl "http://localhost:35248/api/map?id=1234567:buildings&latitude=41.8902&longitude=12.4922"
```

---

## Model Context Protocol (MCP)

Cronotopia exposes a standard Model Context Protocol endpoint at `/mcp` supporting both JSON-RPC 2.0 via HTTP POST and persistent Server-Sent Events via HTTP GET.

### Exposed MCP Tools

1.  **`search_events`**
    *   Find historical events around given coordinates, radius, and year.
    *   *Parameters:* `latitude` (number), `longitude` (number), `radius` (number, default: 50), `year` (int), `limit` (int, default: 20).
2.  **`search_knowledge`**
    *   Perform lexical, entity label, semantic, or hybrid search over Wikipedia articles and Wikidata entities.
    *   *Parameters:* `query` (string), `mode` (`"hybrid"`, `"entity"`, `"lexical"`, `"semantic"`), `limit` (int, default: 10).
3.  **`get_article`**
    *   Retrieve full Wikipedia article sections by ID or Wikidata QID integer.
    *   *Parameters:* `id` (int).
4.  **`get_media`**
    *   Retrieve media content (e.g. book or score) by media ID with optional offset and length.
    *   *Parameters:* `id` (int), `offset` (int, optional), `length` (int, optional).
5.  **`get_map_config`**
    *   Retrieve default center coordinates and min/max available zoom levels.
    *   *Parameters:* None.
6.  **`get_map_centroids`**
    *   Retrieve centroids, bounding boxes, and metadata of vector map features (roads, buildings, water, places, POIs) within a radius.
    *   *Parameters:* `latitude` (number), `longitude` (number), `radius` (number, default: 1000), `zoom` (int), `limit` (int, default: 50).
7.  **`get_map_feature`**
    *   Retrieve a specific map feature with its full geometry coordinates and properties by ID around a given location.
    *   *Parameters:* `id` (string), `latitude` (number), `longitude` (number), `radius` (number, default: 1000), `zoom` (int).

---

## Acknowledgments

*   **[Wikidata](https://www.wikidata.org/):** Source for historical entities, claims, coordinates, and dates.
*   **[Wikipedia](https://www.wikipedia.org/):** Source for encyclopedic text and article structure.
*   **[OpenStreetMap](https://www.openstreetmap.org/):** Geospatial data for vector basemap tiles.
*   **[NOAA ETOPO](https://www.ncei.noaa.gov/products/etopo-global-relief-model):** Global relief model for elevations and bathymetry.
*   **[alphaTab](https://alphatab.net/):** Cross-platform music notation and audio synthesis engine.
*   **[MapLibre GL](https://maplibre.org/):** Client-side vector map renderer and 3D terrain engine.
*   **[SQLite](https://www.sqlite.org/):** Embedded storage engine.
*   **[Qwen Team](https://github.com/QwenLM):** Base embedding model architecture.
