// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type InputArticle struct {
	MainEntity struct {
		Identifier string `json:"identifier"`
	} `json:"main_entity"`
	Name        string `json:"name"`
	ArticleBody struct {
		HTML string `json:"html"`
	} `json:"article_body"`
	Identifier int `json:"identifier"`
}

type byteCounter struct {
	total *int64
}

func (bc *byteCounter) Write(p []byte) (int, error) {
	*bc.total += int64(len(p))
	return len(p), nil
}

type rawSection struct {
	title   string
	content string
	pow     int
}

func runWikipediaImport(src string) error {
	log.Printf("Starting Wikipedia Enterprise import from %s...", src)

	schemaConn := db.pool.Get(context.Background())
	if schemaConn == nil {
		return fmt.Errorf("failed to get connection")
	}
	if err := initKnowledgeSchema(schemaConn); err != nil {
		db.pool.Put(schemaConn)
		return err
	}
	db.pool.Put(schemaConn)

	var reader io.Reader
	var totalSize int64
	var baseCloser io.Closer

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		resp, err := http.Get(src)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		reader = resp.Body
		totalSize = resp.ContentLength
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		stat, _ := f.Stat()
		if stat != nil {
			totalSize = stat.Size()
		}
		reader = f
		baseCloser = f
	}

	bytesRead := int64(0)
	gzReader, err := gzip.NewReader(io.TeeReader(reader, &byteCounter{total: &bytesRead}))
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg {
			if err := processWikipediaJSONL(tarReader); err != nil {
				log.Printf("Error processing %s: %v", header.Name, err)
			}
			if totalSize > 0 {
				log.Printf("Processed: %s (%.1f%%)", header.Name, float64(bytesRead)/float64(totalSize)*100.0)
			}
		}
	}

	if baseCloser != nil {
		baseCloser.Close()
	}

	log.Println("Populating section full text search index (FTS5)...")
	conn := db.pool.Get(context.Background())
	if conn != nil {
		_ = sqlitex.Execute(conn, "INSERT INTO section_search(section_search) VALUES('rebuild')", nil)
		_ = sqlitex.Execute(conn, "INSERT INTO entity_label_search(entity_label_search) VALUES('rebuild')", nil)
		db.pool.Put(conn)
	}

	log.Println("Wikipedia import finished.")
	return nil
}

func processWikipediaJSONL(r io.Reader) error {
	decoder := json.NewDecoder(r)
	conn := db.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get db connection")
	}
	defer db.pool.Put(conn)

	var err error
	deferFn := sqlitex.Transaction(conn)
	count := 0

	for {
		var art InputArticle
		if err := decoder.Decode(&art); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if art.ArticleBody.HTML == "" {
			continue
		}

		qID, err := strconv.Atoi(strings.TrimPrefix(art.MainEntity.Identifier, "Q"))
		if err != nil || qID <= 0 {
			continue
		}

		_ = sqlitex.Execute(conn, `
			INSERT INTO entities (id, article_id, title) VALUES (?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET 
				article_id = excluded.article_id,
				title = excluded.title
		`, &sqlitex.ExecOptions{
			Args: []any{qID, art.Identifier, art.Name},
		})

		doc, err := html.Parse(strings.NewReader(art.ArticleBody.HTML))
		if err == nil {
			sections := extractHTMLSections(doc)
			for _, sec := range sections {
				_ = sqlitex.Execute(conn, `INSERT INTO sections (entity_id, title, content, pow) VALUES (?, ?, ?, ?)`, &sqlitex.ExecOptions{
					Args: []any{qID, sec.title, sec.content, sec.pow},
				})
			}
		}

		count++
		if count >= 1000 {
			deferFn(&err)
			deferFn = sqlitex.Transaction(conn)
			count = 0
		}
	}
	deferFn(&err)
	return nil
}

func extractHTMLSections(node *html.Node) []rawSection {
	var sections []rawSection
	var currentTitle string
	var currentPow int
	var currentTexts []string

	flush := func() {
		if len(currentTexts) > 0 {
			sections = append(sections, rawSection{
				title:   currentTitle,
				content: strings.TrimSpace(strings.Join(currentTexts, "\n\n")),
				pow:     currentPow,
			})
			currentTexts = nil
		}
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h1", "h2", "h3", "h4", "h5", "h6":
				flush()
				currentTitle = extractNodeText(n)
				currentPow = int(n.Data[1] - '0')
				return
			case "p", "li":
				t := strings.TrimSpace(extractNodeText(n))
				if t != "" {
					currentTexts = append(currentTexts, t)
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	flush()
	return sections
}

func extractNodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "style" || c.Data == "script" || c.Data == "sup") {
			continue
		}
		sb.WriteString(extractNodeText(c))
	}
	return sb.String()
}

func initKnowledgeSchema(conn *sqlite.Conn) error {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS entities (
			id INTEGER PRIMARY KEY,
			article_id INTEGER,
			title TEXT,
			description TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_art ON entities(article_id);`,

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

		`CREATE VIRTUAL TABLE IF NOT EXISTS entity_label_search USING fts5(
			title,
			content='entities',
			content_rowid='id',
			tokenize='unicode61 remove_diacritics 2'
		);`,
	}

	for _, s := range sqls {
		if err := sqlitex.ExecuteTransient(conn, s, nil); err != nil {
			return fmt.Errorf("error creating knowledge schema: %w", err)
		}
	}
	return nil
}
