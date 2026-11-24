// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package corpus

import (
	"fmt"

	"cronotopia/acta"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type ArticleResult struct {
	ID        int                    `json:"id"`
	Title     string                 `json:"title,omitempty"`
	EntityID  int                    `json:"entity_id,omitempty"`
	Sections  []ArticleResultSection `json:"sections,omitempty"`
	Latitude  float64                `json:"latitude,omitempty"`
	Longitude float64                `json:"longitude,omitempty"`
	Year      int                    `json:"year,omitempty"`
	DateBegin string                 `json:"date_begin,omitempty"`
	DateEnd   string                 `json:"date_end,omitempty"`
	Media     []any                  `json:"media,omitempty"`
}

type ArticleResultSection struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func GetArticleByEntity(conn *sqlite.Conn, entityID int) (ArticleResult, error) {
	secSchema := acta.TableSchema(conn, "sections")
	entSchema := acta.TableSchema(conn, "entities")

	targetTitle := ""
	_ = sqlitex.Execute(conn, fmt.Sprintf(`
		SELECT COALESCE(NULLIF(e.title, ''), NULLIF(s.title, ''), 'Untitled')
		FROM %s.sections s
		LEFT JOIN %s.entities e ON s.entity_id = e.id
		WHERE s.entity_id = ?
		LIMIT 1
	`, secSchema, entSchema), &sqlitex.ExecOptions{
		Args: []any{entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			targetTitle = stmt.ColumnText(0)
			return nil
		},
	})

	var res ArticleResult
	err := sqlitex.Execute(conn, fmt.Sprintf(`
		SELECT id, title, content
		FROM %s.sections
		WHERE entity_id = ?
		ORDER BY id ASC
	`, secSchema), &sqlitex.ExecOptions{
		Args: []any{entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			res.Sections = append(res.Sections, ArticleResultSection{
				ID:      int(stmt.ColumnInt64(0)),
				Title:   stmt.ColumnText(1),
				Content: stmt.ColumnText(2),
			})
			return nil
		},
	})
	if err != nil {
		return res, err
	}
	if len(res.Sections) == 0 {
		return res, fmt.Errorf("article not found")
	}

	if targetTitle == "" {
		targetTitle = "Untitled"
	}
	res.ID = entityID
	res.EntityID = entityID
	res.Title = targetTitle

	info := acta.GetEntityInfo(conn, res.EntityID, 0, 0)
	res.Latitude = info.Lat
	res.Longitude = info.Lon
	res.Year = info.YearBegin
	res.DateBegin = info.DateBegin
	res.DateEnd = info.DateEnd
	for _, m := range info.Media {
		res.Media = append(res.Media, m)
	}
	return res, nil
}

func GetArticleByArticle(conn *sqlite.Conn, articleID int) (ArticleResult, error) {
	entSchema := acta.TableSchema(conn, "entities")
	targetEntityID := 0
	_ = sqlitex.Execute(conn, fmt.Sprintf(`SELECT id FROM %s.entities WHERE article_id = ? LIMIT 1`, entSchema), &sqlitex.ExecOptions{
		Args: []any{articleID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			targetEntityID = int(stmt.ColumnInt64(0))
			return nil
		},
	})
	if targetEntityID == 0 {
		return ArticleResult{}, fmt.Errorf("article not found")
	}

	res, err := GetArticleByEntity(conn, targetEntityID)
	if err != nil {
		return res, err
	}
	res.ID = articleID
	return res, nil
}

func GetArticle(conn *sqlite.Conn, entityOrArticleID int) (ArticleResult, error) {
	res, err := GetArticleByEntity(conn, entityOrArticleID)
	if err == nil && len(res.Sections) > 0 {
		return res, nil
	}
	return GetArticleByArticle(conn, entityOrArticleID)
}
