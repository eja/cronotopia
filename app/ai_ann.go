// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const VectorsPerCentroid = 2500

type VectorDistance struct {
	ID            int64
	ChunkRowID    int64
	ChunkPosition int
	Distance      float32
}

func (h *DBHandler) AiHasANN() bool {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return false
	}
	defer h.pool.Put(conn)

	var found bool
	_ = sqlitex.Execute(conn, "SELECT id FROM vectors_ann_index LIMIT 1", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			found = true
			return nil
		},
	})
	return found
}

func (h *DBHandler) ProcessEmbeddings() error {
	const batchSize = 250
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

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
			SELECT s.id, COALESCE(e.article_title, s.title), s.content 
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

		for _, item := range items {
			emb, err := aiEmbeddings(options.aiModelPrefixSave + item.text)
			if err != nil {
				continue
			}
			_ = sqlitex.Execute(conn, "INSERT OR REPLACE INTO vectors (id, embedding) VALUES (?, ?)", &sqlitex.ExecOptions{
				Args: []any{item.id, Float32ToBytes(emb)},
			})
		}
		log.Printf("Embedding progress: %d/%d (%.1f%%)", end, total, float64(end)/float64(total)*100.0)
	}

	if options.aiAnn {
		return h.ProcessANN()
	}
	return nil
}

func (h *DBHandler) ProcessANN() error {
	size := options.aiAnnSize
	if size <= 0 {
		size = 64
	}

	conn := h.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

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

	k := total / VectorsPerCentroid
	if k < 1 {
		k = 1
	}

	centroids := make([][]float32, k)
	for i := range k {
		centroids[i] = make([]float32, size)
		copy(centroids[i], items[(i*total)/k].emb)
	}

	assignments := make([]int, total)
	workers := runtime.NumCPU()

	for iter := 0; iter < 10; iter++ {
		var wg sync.WaitGroup
		chunk := (total + workers - 1) / workers

		for w := 0; w < workers; w++ {
			s := w * chunk
			e := min(s+chunk, total)
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
			}(s, e)
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
		for start := 0; start < len(group); start += VectorsPerCentroid {
			end := min(start+VectorsPerCentroid, len(group))
			sub := group[start:end]
			chunkID++

			var chunkBlob []byte
			for pos, it := range sub {
				chunkBlob = append(chunkBlob, Float32ToBytes(it.emb)...)
				_ = sqlitex.Execute(conn, "INSERT INTO vectors_ann_index (vectors_id, chunk_id, chunk_position) VALUES (?, ?, ?)", &sqlitex.ExecOptions{
					Args: []any{it.id, chunkID, pos},
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

func (h *DBHandler) SearchAnn(vectors []float32, size int, limit int) ([]VectorDistance, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	type centroidDist struct {
		id   int
		dist float32
	}
	var cDists []centroidDist

	_ = sqlitex.Execute(conn, "SELECT id, centroid FROM vectors_ann_centroids", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			cLen := stmt.ColumnLen(1)
			cBytes := make([]byte, cLen)
			stmt.ColumnBytes(1, cBytes)
			cFloats := BytesToFloat32(cBytes)

			if size <= 0 || size > len(cFloats) {
				size = len(cFloats)
			}

			mrlQuery := make([]float32, size)
			copy(mrlQuery, vectors[:size])
			l2Norm(mrlQuery)

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

	var selectedChunks []string
	for i := 0; i < len(cDists) && i < 3; i++ {
		_ = sqlitex.Execute(conn, "SELECT chunk_id FROM vectors_ann_centroid_chunks WHERE centroid_id = ?", &sqlitex.ExecOptions{
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
	mrlQuery := make([]float32, size)
	copy(mrlQuery, vectors[:size])
	l2Norm(mrlQuery)

	queryStr := "SELECT id, chunk FROM vectors_ann_chunks WHERE id IN (" + strings.Join(selectedChunks, ",") + ")"
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
