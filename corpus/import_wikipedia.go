// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package corpus

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
	"zombiezen.com/go/sqlite/sqlitex"
)

type inputArticle struct {
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

func ImportWikipedia(pool *sqlitex.Pool, src string) error {
	log.Printf("Starting Wikipedia Enterprise import from %s...", src)

	schemaConn := pool.Get(context.Background())
	if schemaConn == nil {
		return fmt.Errorf("failed to get connection")
	}
	if err := InitSchema(schemaConn); err != nil {
		pool.Put(schemaConn)
		return err
	}
	pool.Put(schemaConn)

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
			if err := processWikipediaJSONL(pool, tarReader); err != nil {
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
	conn := pool.Get(context.Background())
	if conn != nil {
		_ = sqlitex.Execute(conn, "INSERT INTO section_search(section_search) VALUES('rebuild')", nil)
		pool.Put(conn)
	}

	log.Println("Wikipedia import finished.")
	return nil
}

func processWikipediaJSONL(pool *sqlitex.Pool, r io.Reader) error {
	decoder := json.NewDecoder(r)
	conn := pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get db connection")
	}
	defer pool.Put(conn)

	var err error
	deferFn := sqlitex.Transaction(conn)
	count := 0

	for {
		var art inputArticle
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
