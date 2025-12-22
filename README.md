# Cronotopia

An high-performance, offline-first spatio-temporal database engine and map visualization server written in Go. 

It is designed to ingest massive datasets from Wikidata, extract entities with temporal and geospatial properties, and serve them alongside embedded vector map tiles from a single, portable SQLite database.

## Overview

Cronotopia is built to map history without relying on external dependencies or internet connectivity. It combines historical data processing with a full-stack map server. By storing both the extracted Wikidata entities and pre-rendered vector tiles in a single SQLite file, Cronotopia allows for a completely self-contained deployment—perfect for offline environments, archival projects, or private networks.

## Key Features

*   **100% Offline Capable:** Does not require external map providers or internet access to function once the database is built.
*   **Single-File Architecture:** All data—historical records, search indices, and map vector tiles—are stored in one unified SQLite database file.
*   **Integrated Vector Tile Server:** Serves geospatial tiles directly from the database to the MapLibre frontend (PBF/MVT format).
*   **Efficient Ingestion:** Stream-based processing of large Wikidata JSON dumps.
*   **Spatio-Temporal Search:** Custom SQL logic optimized for querying events by year, specific dates, and geographic proximity.

## Installation

### Building from Source

Clone the repository and build the binary:

```bash
git clone https://github.com/eja/cronotopia.git
cd cronotopia
make
```

## Usage

Cronotopia operates in two modes: **Import Mode** and **Server Mode**.

### 1. Data Import

To populate the database with historical data, provide a URL or a local path to a Wikidata entity dump.
*Note: The target SQLite database should already contain the vector tiles table (`tiles`) if you intend to use the map offline, any mbtiles db will work.*

```bash
# Import from a local file with specific language targeting
./cronotopia --log --language "en,fr,es" --import ./wikidata-20251215-all.json.gz --db cronotopia.db
```

**Options:**
*   `--import`: Path or URL to the source dump.
*   `--db`: Path to the SQLite database (containing map tiles).
*   `--language`: Comma-separated list of ISO language codes to import.
*   `--log`: Enable verbose logging to stdout.

### 2. Server Mode

Run the application without the `--import` flag to start the web server. This hosts the API, the Tile Server, and the MapLibre frontend.

```bash
./cronotopia --web-host localhost --web-port 35248 --db cronotopia.db
```

The web interface will be accessible at `http://localhost:35248`.

## API Reference

The application exposes a JSON API at `/api` for querying historical data.

### Endpoint: `GET /api`

**Parameters:**

| Parameter | Type | Description |
| :--- | :--- | :--- |
| `query` | string | Text search for entity labels (e.g., "Battle of"). |
| `day` | int | The target day. |
| `month` | int | The target month. |
| `year` | int | The target year. |
| `latitude` | float | Geographic latitude for proximity sorting. |
| `longitude` | float | Geographic longitude for proximity sorting. |
| `range` | int | Temporal logic: `0` (Specific), `1` (Before), `2` (After). |
| `limit` | int | Maximum number of results to return (default: 100). |
