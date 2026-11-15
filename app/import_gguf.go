// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func ImportGGUFToDB(db *DBHandler, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	return ImportGGUFFromReader(db, f)
}

func ImportGGUFFromReader(db *DBHandler, r ReadSeekReaderAt) error {
	parser, err := NewGGUFParser(r)
	if err != nil {
		return fmt.Errorf("failed to parse GGUF: %w", err)
	}

	conn := db.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("database busy")
	}
	defer db.pool.Put(conn)

	if err := initAISchema(conn); err != nil {
		return err
	}

	if err := db.AiClearModel(); err != nil {
		return err
	}

	metaJSON, err := json.Marshal(parser.Metadata)
	if err != nil {
		return fmt.Errorf("failed to serialize metadata: %w", err)
	}
	_ = db.SettingPut("ai_metadata", string(metaJSON))

	arch, _ := parser.Metadata["general.architecture"].(string)
	if arch == "" {
		arch = "qwen2"
	}
	_ = db.SettingPut("ai_arch", arch)

	var embDim uint64
	if v, ok := parser.Metadata[arch+".embedding_length"]; ok {
		embDim = toUint64(v)
	}
	if embDim > 0 {
		_ = db.SettingPut("ai_dims", strconv.FormatUint(embDim, 10))
	}

	var numLayers uint64
	if v, ok := parser.Metadata[arch+".block_count"]; ok {
		numLayers = toUint64(v)
	}
	if numLayers > 0 {
		_ = db.SettingPut("ai_layers", strconv.FormatUint(numLayers, 10))
	}

	endTx, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer endTx(&err)

	totalTensors := len(parser.Tensors)
	log.Printf("Importing %d tensors into database...", totalTensors)

	idx := 0
	nextMilestone := 10
	for _, ti := range parser.Tensors {
		idx++

		if ti.Name == "output.weight" {
			log.Printf("Skipping unused generative tensor: %s", ti.Name)
			continue
		}

		layer := parseLayerIndex(ti.Name)

		sizeBytes, err := tensorSizeBytes(ti.Type, ti.Dimensions)
		if err != nil {
			return fmt.Errorf("tensor %s: %w", ti.Name, err)
		}

		data := make([]byte, sizeBytes)
		offset := parser.DataStart + int64(ti.Offset)
		if _, err := r.ReadAt(data, offset); err != nil {
			return fmt.Errorf("error reading tensor %s: %w", ti.Name, err)
		}

		dimStrs := make([]string, len(ti.Dimensions))
		for i, d := range ti.Dimensions {
			dimStrs[i] = strconv.FormatUint(d, 10)
		}

		tensorRecord := ModelTensor{
			Layer: layer,
			Name:  ti.Name,
			DType: int(ti.Type),
			Dims:  strings.Join(dimStrs, ","),
			Data:  data,
		}

		if err := db.AiSaveTensor(conn, tensorRecord); err != nil {
			return fmt.Errorf("error writing tensor %s: %w", ti.Name, err)
		}

		if totalTensors > 0 {
			pct := idx * 100 / totalTensors
			if pct >= nextMilestone && nextMilestone <= 100 {
				log.Printf("Tensor import progress: %d%% (%d/%d)", nextMilestone, idx, totalTensors)
				nextMilestone = (pct/10)*10 + 10
			}
		}
	}

	return nil
}

func initAISchema(conn *sqlite.Conn) error {
	sqls := []string{
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
			return fmt.Errorf("error creating AI schema: %w", err)
		}
	}
	return nil
}
