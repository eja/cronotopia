// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	globalTok *bpeTokenizer
	globalMdl *qwen3Model
	globalMu  sync.RWMutex
)

type aiEmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type aiEmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func aiEmbeddings(input string) ([]float32, error) {
	if options.aiApi {
		return aiApiEmbeddings(input)
	}
	return localAiEmbeddings(input)
}

func aiApiEmbeddings(input string) ([]float32, error) {
	payload := aiEmbeddingRequest{Model: options.aiModel, Input: []string{input}}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(context.TODO(), "POST", options.aiApiUrl, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if options.aiApiKey != "" {
		req.Header.Set("Authorization", "Bearer "+options.aiApiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res aiEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if len(res.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned: %s", res.Error.Message)
	}
	return res.Data[0].Embedding, nil
}

func aiInit() error {
	if options.aiApi {
		return nil
	}
	return localAiInit()
}

func localAiInit() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if !db.AiHasModel() {
		return fmt.Errorf("no AI model found in database")
	}

	metaJSON, err := db.SettingGet("ai_metadata")
	if err != nil {
		return fmt.Errorf("ai_metadata not found in database: %w", err)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	tensors, err := db.AiLoadTensorInfos()
	if err != nil {
		return fmt.Errorf("failed to load tensor info: %w", err)
	}

	p := &GGUFParser{
		Metadata: meta,
		Tensors:  tensors,
	}

	arch, _ := db.SettingGet("ai_arch")
	dimStr, _ := db.SettingGet("ai_dims")
	layersStr, _ := db.SettingGet("ai_layers")
	log.Printf("AI: Initializing model from database (arch: %s, dim: %s, layers: %s)", arch, dimStr, layersStr)

	tok, err := loadTokenizer(p)
	if err != nil {
		return fmt.Errorf("failed to load tokenizer: %w", err)
	}
	mdl, err := loadModel(p)
	if err != nil {
		return fmt.Errorf("failed to load model: %w", err)
	}

	globalTok = tok
	globalMdl = mdl

	if options.aiCache {
		log.Println("AI: Operating in cached RAM mode")
	} else {
		log.Println("AI: Operating in low-RAM mode (layer-by-layer)")
	}

	return nil
}

func localAiEmbeddings(input string) ([]float32, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if globalTok == nil || globalMdl == nil {
		return nil, fmt.Errorf("local AI model not loaded")
	}

	ids := globalTok.encode(input)
	if len(ids) == 0 {
		return nil, fmt.Errorf("empty input tokens")
	}

	emb, err := globalMdl.embed(ids)
	if err != nil {
		return nil, err
	}

	return emb, nil
}
