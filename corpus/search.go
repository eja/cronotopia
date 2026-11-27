// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package corpus

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"cronotopia/vfs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type SectionMatch struct {
	EntityID  int
	ArticleID int
	SectionID int
	Title     string
	Content   string
	Snippet   string
	Score     float64
}

func SanitizeQuery(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	var sanitized []string
	for _, w := range words {
		clean := strings.ReplaceAll(w, `"`, `""`)
		sanitized = append(sanitized, `"`+clean+`"`)
	}
	return strings.Join(sanitized, " ")
}

func Snippet(val string, maxLen int) string {
	if utf8.RuneCountInString(val) > maxLen {
		runes := []rune(val)
		return string(runes[:maxLen]) + "..."
	}
	return val
}

func NormalizeBM25(score float64) float64 {
	rawScore := -score
	if rawScore < 0 {
		rawScore = 0
	}
	const c = 0.1
	return (1.0 - math.Exp(-c*rawScore)) * 100.0
}

func SearchLexical(conn *sqlite.Conn, searchQuery string, limit int) ([]SectionMatch, error) {
	sanitized := SanitizeQuery(searchQuery)
	if sanitized == "" {
		return nil, nil
	}

	ftsSchema := vfs.TableSchema(conn, "section_search")
	if !vfs.HasSchemaTable(conn, ftsSchema, "section_search") {
		return nil, nil
	}

	secSchema := vfs.TableSchema(conn, "sections")
	entSchema := vfs.TableSchema(conn, "entities")

	if limit <= 0 {
		limit = 10
	}

	sqlQuery := fmt.Sprintf(`
		SELECT
			s.entity_id,
			COALESCE(e.title, s.title),
			COALESCE(e.article_id, 0),
			s.content,
			snippet(section_search, 1, '<mark>', '</mark>', '...', 32) as snippet,
			bm25(section_search) as power,
			s.id
		FROM %s.section_search
		JOIN %s.sections s ON section_search.rowid = s.id
		LEFT JOIN %s.entities e ON s.entity_id = e.id
		WHERE section_search MATCH ?
		ORDER BY power ASC
		LIMIT ?
	`, ftsSchema, secSchema, entSchema)

	var results []SectionMatch

	err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
		Args: []any{sanitized, limit},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			res := SectionMatch{
				EntityID:  int(stmt.ColumnInt64(0)),
				Title:     stmt.ColumnText(1),
				ArticleID: int(stmt.ColumnInt64(2)),
				Content:   stmt.ColumnText(3),
				Snippet:   stmt.ColumnText(4),
				Score:     NormalizeBM25(stmt.ColumnFloat(5)),
				SectionID: int(stmt.ColumnInt64(6)),
			}
			if res.Snippet == "" {
				res.Snippet = Snippet(res.Content, 160)
			}
			results = append(results, res)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func SearchVectors(conn *sqlite.Conn, eng *Engine, query string, limit int) ([]SectionMatch, error) {
	if eng == nil || !eng.HasANN(conn) {
		return []SectionMatch{}, nil
	}

	if limit <= 0 {
		limit = 10
	}

	fullQuery := eng.config.ModelPrefixSearch + query
	queryEmbedding, err := eng.Embeddings(fullQuery)
	if err != nil {
		return nil, err
	}

	topAnnResults, err := eng.SearchAnn(conn, queryEmbedding, eng.config.AnnSize, limit*6)
	if err != nil {
		return nil, err
	}

	annIndexSchema := vfs.TableSchema(conn, "vectors_ann_index")
	secSchema := vfs.TableSchema(conn, "sections")
	entSchema := vfs.TableSchema(conn, "entities")

	results := make([]SectionMatch, 0, limit)
	seenEntity := make(map[int]bool)

	for _, v := range topAnnResults {
		var vectorID int64
		_ = sqlitex.Execute(conn, fmt.Sprintf("SELECT vectors_id FROM %s.vectors_ann_index WHERE chunk_id = ? AND chunk_position = ? LIMIT 1", annIndexSchema), &sqlitex.ExecOptions{
			Args: []any{v.ChunkRowID, v.ChunkPosition},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				vectorID = stmt.ColumnInt64(0)
				return nil
			},
		})
		if vectorID == 0 {
			continue
		}

		var res SectionMatch
		querySQL := fmt.Sprintf(`
			SELECT s.entity_id, COALESCE(e.title, s.title), s.content, COALESCE(e.article_id, 0), s.id
			FROM %s.sections s
			LEFT JOIN %s.entities e ON s.entity_id = e.id
			WHERE s.id = ?
		`, secSchema, entSchema)
		_ = sqlitex.Execute(conn, querySQL, &sqlitex.ExecOptions{
			Args: []any{vectorID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				res.EntityID = int(stmt.ColumnInt64(0))
				res.Title = stmt.ColumnText(1)
				res.Content = stmt.ColumnText(2)
				res.ArticleID = int(stmt.ColumnInt64(3))
				res.SectionID = int(stmt.ColumnInt64(4))
				return nil
			},
		})

		if res.EntityID == 0 || seenEntity[res.EntityID] {
			continue
		}

		seenEntity[res.EntityID] = true
		res.Snippet = Snippet(res.Content, 160)
		res.Score = math.Max(0, math.Min(100, float64((v.Distance+1.0)/2.0*100.0)))
		results = append(results, res)

		if len(results) >= limit {
			break
		}
	}
	return results, nil
}
