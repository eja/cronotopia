// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cronotopia/event"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Config struct {
	Model             string
	ModelPrefixSave   string
	ModelPrefixSearch string
	Ann               bool
	AnnSize           int
	Api               bool
	ApiKey            string
	ApiUrl            string
	Cache             bool
	Threads           int
}

type Engine struct {
	mu     sync.RWMutex
	tok    *bpeTokenizer
	mdl    *qwen3Model
	config Config
}

type VectorDistance struct {
	ID            int64
	ChunkRowID    int64
	ChunkPosition int
	Distance      float32
}

func NewEngine(cfg Config) *Engine {
	return &Engine{
		config: cfg,
	}
}

func InitSchema(conn *sqlite.Conn) error {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS sections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_id INTEGER NOT NULL,
			title TEXT,
			content TEXT,
			pow INTEGER DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sections_entity ON sections(entity_id);`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS section_search USING fts5(
			title,
			content,
			content='sections',
			content_rowid='id'
		);`,

		`CREATE TABLE IF NOT EXISTS vectors (
			id INTEGER PRIMARY KEY,
			embedding BLOB
		);`,
		`CREATE TABLE IF NOT EXISTS vectors_ann_chunks (
			id INTEGER PRIMARY KEY,
			chunk BLOB
		);`,
		`CREATE TABLE IF NOT EXISTS vectors_ann_index (
			vectors_id INTEGER NOT NULL,
			chunk_id INTEGER NOT NULL,
			chunk_position INTEGER NOT NULL,
			PRIMARY KEY (chunk_id, chunk_position)
		) WITHOUT ROWID;`,
		`CREATE TABLE IF NOT EXISTS vectors_ann_centroids (
			id INTEGER PRIMARY KEY,
			centroid BLOB
		);`,
		`CREATE TABLE IF NOT EXISTS vectors_ann_centroid_chunks (
			centroid_id INTEGER NOT NULL,
			chunk_id INTEGER NOT NULL,
			PRIMARY KEY(centroid_id, chunk_id)
		) WITHOUT ROWID;`,

		`CREATE TABLE IF NOT EXISTS tensors (
			layer INTEGER NOT NULL,
			name TEXT NOT NULL,
			dtype INTEGER NOT NULL,
			dims TEXT NOT NULL,
			data BLOB NOT NULL
		);`,
	}

	for _, s := range sqls {
		if err := sqlitex.ExecuteTransient(conn, s, nil); err != nil {
			return fmt.Errorf("error creating corpus schema: %w", err)
		}
	}
	return nil
}

func (e *Engine) Init(conn *sqlite.Conn) error {
	if e.config.Api {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	schema := event.TableSchema(conn, "tensors")
	if !HasModel(conn, schema) {
		return fmt.Errorf("no AI model found in database")
	}

	metaTbl := "settings"
	if schema != "" && schema != "main" {
		metaTbl = schema + ".settings"
	}

	var metaJSON string
	err := sqlitex.Execute(conn, fmt.Sprintf("SELECT value FROM %s WHERE key = 'ai_metadata' LIMIT 1", metaTbl), &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			metaJSON = stmt.ColumnText(0)
			return nil
		},
	})
	if err != nil || metaJSON == "" {
		return fmt.Errorf("ai_metadata not found in database: %w", err)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	tensors, err := LoadTensorInfos(conn, schema)
	if err != nil {
		return fmt.Errorf("failed to load tensor info: %w", err)
	}

	p := &GGUFParser{
		Metadata: meta,
		Tensors:  tensors,
		ReadBlob: func(rowID int64, offset int64, dest []byte) error {
			return GetTensorBlob(conn, schema, rowID, offset, dest)
		},
	}

	tok, err := loadTokenizer(p)
	if err != nil {
		return fmt.Errorf("failed to load tokenizer: %w", err)
	}
	mdl, err := loadModel(p, e.config.Cache)
	if err != nil {
		return fmt.Errorf("failed to load model: %w", err)
	}

	e.tok = tok
	e.mdl = mdl
	return nil
}

func (e *Engine) Embeddings(input string) ([]float32, error) {
	if e.config.Api {
		return e.apiEmbeddings(input)
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.tok == nil || e.mdl == nil {
		return nil, fmt.Errorf("local AI model not loaded")
	}

	ids := e.tok.encode(input)
	if len(ids) == 0 {
		return nil, fmt.Errorf("empty input tokens")
	}
	return e.mdl.embed(ids)
}

func (e *Engine) apiEmbeddings(input string) ([]float32, error) {
	type reqPayload struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	type respPayload struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	payload := reqPayload{Model: e.config.Model, Input: []string{input}}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(context.TODO(), "POST", e.config.ApiUrl, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.config.ApiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.config.ApiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res respPayload
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if len(res.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned: %s", res.Error.Message)
	}
	return res.Data[0].Embedding, nil
}

func (e *Engine) ProcessEmbeddings(pool *sqlitex.Pool) error {
	const batchSize = 250
	conn := pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer pool.Put(conn)

	if err := InitSchema(conn); err != nil {
		return err
	}

	var pendingSectionIDs []int
	err := sqlitex.Execute(conn, `
		SELECT s.id FROM sections s 
		WHERE s.id NOT IN (SELECT id FROM vectors)
		ORDER BY s.id
	`, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			pendingSectionIDs = append(pendingSectionIDs, int(stmt.ColumnInt64(0)))
			return nil
		},
	})
	if err != nil {
		return err
	}

	total := len(pendingSectionIDs)
	log.Printf("Processing embeddings for %d sections...", total)
	if total == 0 {
		return nil
	}

	for i := 0; i < total; i += batchSize {
		end := min(i+batchSize, total)
		chunkIDs := pendingSectionIDs[i:end]

		placeholders := make([]string, len(chunkIDs))
		args := make([]any, len(chunkIDs))
		for idx, id := range chunkIDs {
			placeholders[idx] = "?"
			args[idx] = id
		}

		type secItem struct {
			id   int
			text string
		}
		var items []secItem

		query := fmt.Sprintf(`
			SELECT s.id, COALESCE(e.title, s.title), s.content 
			FROM sections s
			LEFT JOIN entities e ON s.entity_id = e.id
			WHERE s.id IN (%s)
		`, strings.Join(placeholders, ","))

		_ = sqlitex.Execute(conn, query, &sqlitex.ExecOptions{
			Args: args,
			ResultFunc: func(stmt *sqlite.Stmt) error {
				items = append(items, secItem{
					id:   int(stmt.ColumnInt64(0)),
					text: stmt.ColumnText(1) + "\n\n" + stmt.ColumnText(2),
				})
				return nil
			},
		})

		endTx, txErr := sqlitex.ImmediateTransaction(conn)
		if txErr == nil {
			for _, item := range items {
				emb, err := e.Embeddings(e.config.ModelPrefixSave + item.text)
				if err != nil {
					continue
				}
				_ = sqlitex.Execute(conn, "INSERT OR REPLACE INTO vectors (id, embedding) VALUES (?, ?)", &sqlitex.ExecOptions{
					Args: []any{item.id, Float32ToBytes(emb)},
				})
			}
			endTx(&txErr)
		}
		log.Printf("Embedding progress: %d/%d (%.1f%%)", end, total, float64(end)/float64(total)*100.0)
	}

	if e.config.Ann {
		return e.ProcessANN(conn)
	}
	return nil
}

func (e *Engine) ProcessANN(conn *sqlite.Conn) error {
	size := e.config.AnnSize
	if size <= 0 {
		size = 64
	}

	_ = sqlitex.Execute(conn, "DELETE FROM vectors_ann_index", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM vectors_ann_chunks", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM vectors_ann_centroids", nil)
	_ = sqlitex.Execute(conn, "DELETE FROM vectors_ann_centroid_chunks", nil)

	type mrlItem struct {
		id  int
		emb []float32
	}
	var items []mrlItem

	_ = sqlitex.Execute(conn, "SELECT id, embedding FROM vectors ORDER BY id", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			b := make([]byte, stmt.ColumnLen(1))
			stmt.ColumnBytes(1, b)
			full := BytesToFloat32(b)
			if len(full) < size {
				return nil
			}
			cut := make([]float32, size)
			copy(cut, full[:size])
			l2Norm(cut)
			items = append(items, mrlItem{id: int(stmt.ColumnInt64(0)), emb: cut})
			return nil
		},
	})

	total := len(items)
	if total == 0 {
		return nil
	}

	const vectorsPerCentroid = 2500
	k := total / vectorsPerCentroid
	if k < 1 {
		k = 1
	}

	centroids := make([][]float32, k)
	for i := range k {
		centroids[i] = make([]float32, size)
		copy(centroids[i], items[(i*total)/k].emb)
	}

	assignments := make([]int, total)
	workers := e.config.Threads
	if workers <= 0 {
		workers = 4
	}

	for iter := 0; iter < 10; iter++ {
		var wg sync.WaitGroup
		chunk := (total + workers - 1) / workers

		for w := 0; w < workers; w++ {
			s := w * chunk
			endIdx := min(s+chunk, total)
			if s >= total {
				break
			}
			wg.Add(1)
			go func(start, end int) {
				defer wg.Done()
				for i := start; i < end; i++ {
					best := 0
					var bestSim float32 = -1.0
					for cIdx, c := range centroids {
						sim := dot(items[i].emb, c)
						if sim > bestSim {
							bestSim = sim
							best = cIdx
						}
					}
					assignments[i] = best
				}
			}(s, endIdx)
		}
		wg.Wait()

		next := make([][]float32, k)
		counts := make([]int, k)
		for i := range k {
			next[i] = make([]float32, size)
		}
		for i, it := range items {
			c := assignments[i]
			for d := range size {
				next[c][d] += it.emb[d]
			}
			counts[c]++
		}
		for i := range k {
			if counts[i] > 0 {
				l2Norm(next[i])
				centroids[i] = next[i]
			}
		}
	}

	endTx, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer endTx(&err)

	for cIdx, c := range centroids {
		_ = sqlitex.Execute(conn, "INSERT INTO vectors_ann_centroids (id, centroid) VALUES (?, ?)", &sqlitex.ExecOptions{
			Args: []any{cIdx + 1, Float32ToBytes(c)},
		})
	}

	groups := make([][]mrlItem, k)
	for i, it := range items {
		groups[assignments[i]] = append(groups[assignments[i]], it)
	}

	var chunkID int
	for cIdx, group := range groups {
		for start := 0; start < len(group); start += vectorsPerCentroid {
			end := min(start+vectorsPerCentroid, len(group))
			sub := group[start:end]
			chunkID++

			var chunkBlob []byte
			for pos, it := range sub {
				chunkBlob = append(chunkBlob, Float32ToBytes(it.emb)...)
				_ = sqlitex.Execute(conn, "INSERT INTO vectors_ann_index (chunk_id, chunk_position, vectors_id) VALUES (?, ?, ?)", &sqlitex.ExecOptions{
					Args: []any{chunkID, pos, it.id},
				})
			}

			_ = sqlitex.Execute(conn, "INSERT INTO vectors_ann_chunks (id, chunk) VALUES (?, ?)", &sqlitex.ExecOptions{
				Args: []any{chunkID, chunkBlob},
			})
			_ = sqlitex.Execute(conn, "INSERT INTO vectors_ann_centroid_chunks (centroid_id, chunk_id) VALUES (?, ?)", &sqlitex.ExecOptions{
				Args: []any{cIdx + 1, chunkID},
			})
		}
	}

	log.Printf("ANN indexing complete with %d centroids and %d chunks.", k, chunkID)
	return nil
}

func (e *Engine) HasANN(conn *sqlite.Conn) bool {
	schema := event.TableSchema(conn, "vectors_ann_index")
	return event.HasSchemaTable(conn, schema, "vectors_ann_index")
}

func (e *Engine) SearchAnn(conn *sqlite.Conn, vectors []float32, size int, limit int) ([]VectorDistance, error) {
	if size <= 0 || size > len(vectors) {
		size = len(vectors)
	}

	mrlQuery := make([]float32, size)
	copy(mrlQuery, vectors[:size])
	l2Norm(mrlQuery)

	centSchema := event.TableSchema(conn, "vectors_ann_centroids")
	chunkSchema := event.TableSchema(conn, "vectors_ann_chunks")
	linkSchema := event.TableSchema(conn, "vectors_ann_centroid_chunks")

	type centroidDist struct {
		id   int
		dist float32
	}
	var cDists []centroidDist

	_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT id, centroid FROM %s.vectors_ann_centroids", centSchema), &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			cLen := stmt.ColumnLen(1)
			cBytes := make([]byte, cLen)
			stmt.ColumnBytes(1, cBytes)
			cFloats := BytesToFloat32(cBytes)

			if len(cFloats) < size {
				return nil
			}
			cDists = append(cDists, centroidDist{
				id:   int(stmt.ColumnInt64(0)),
				dist: dot(mrlQuery, cFloats[:size]),
			})
			return nil
		},
	})

	if len(cDists) == 0 {
		return nil, nil
	}

	sort.Slice(cDists, func(i, j int) bool {
		return cDists[i].dist > cDists[j].dist
	})

	totalCentroids := len(cDists)
	nprobe := totalCentroids / 10
	if nprobe < 16 {
		nprobe = 16
	}
	if nprobe > 64 {
		nprobe = 64
	}
	if nprobe > totalCentroids {
		nprobe = totalCentroids
	}

	var selectedChunks []string
	for i := 0; i < len(cDists) && i < nprobe; i++ {
		_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT chunk_id FROM %s.vectors_ann_centroid_chunks WHERE centroid_id = ?", linkSchema), &sqlitex.ExecOptions{
			Args: []any{cDists[i].id},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				selectedChunks = append(selectedChunks, strconv.FormatInt(stmt.ColumnInt64(0), 10))
				return nil
			},
		})
	}

	if len(selectedChunks) == 0 {
		return nil, nil
	}

	chunkSize := size * 4
	queryStr := fmt.Sprintf("SELECT id, chunk FROM %s.vectors_ann_chunks WHERE id IN ("+strings.Join(selectedChunks, ",")+")", chunkSchema)
	var annResults []VectorDistance

	_ = sqlitex.Execute(conn, queryStr, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			chunkID := stmt.ColumnInt64(0)
			bLen := stmt.ColumnLen(1)
			chunkBlob := make([]byte, bLen)
			stmt.ColumnBytes(1, chunkBlob)

			for pos := 0; pos+chunkSize <= len(chunkBlob); pos += chunkSize {
				storedFloats := BytesToFloat32(chunkBlob[pos : pos+chunkSize])
				annResults = append(annResults, VectorDistance{
					ChunkRowID:    chunkID,
					ChunkPosition: pos / chunkSize,
					Distance:      dot(mrlQuery, storedFloats),
				})
			}
			return nil
		},
	})

	sort.Slice(annResults, func(i, j int) bool {
		return annResults[i].Distance > annResults[j].Distance
	})

	if limit > 0 && len(annResults) > limit {
		annResults = annResults[:limit]
	}
	return annResults, nil
}
