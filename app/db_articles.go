// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"

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
}

type ArticleResultSection struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (h *DBHandler) ArticleGetByEntity(entityID int) (ArticleResult, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return ArticleResult{}, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	targetTitle := ""
	_ = sqlitex.Execute(conn, `
		SELECT COALESCE(e.article_title, s.title, '')
		FROM sections s
		LEFT JOIN entities e ON s.entity_id = e.id
		WHERE s.entity_id = ?
		LIMIT 1
	`, &sqlitex.ExecOptions{
		Args: []any{entityID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			targetTitle = stmt.ColumnText(0)
			return nil
		},
	})

	if targetTitle == "" {
		return ArticleResult{}, fmt.Errorf("article not found")
	}

	var res ArticleResult
	res.ID = entityID
	res.EntityID = entityID
	res.Title = targetTitle

	err := sqlitex.Execute(conn, `
		SELECT id, title, content
		FROM sections
		WHERE entity_id = ?
		ORDER BY id ASC
	`, &sqlitex.ExecOptions{
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

	info := h.GetEntityInfo(conn, res.EntityID, 0, 0)
	res.Latitude = info.Lat
	res.Longitude = info.Lon
	res.Year = info.YearBegin
	res.DateBegin = info.DateBegin
	res.DateEnd = info.DateEnd

	return res, nil
}

func (h *DBHandler) ArticleGetByArticle(articleID int) (ArticleResult, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return ArticleResult{}, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	targetEntityID := 0
	_ = sqlitex.Execute(conn, `SELECT id FROM entities WHERE article_id = ? LIMIT 1`, &sqlitex.ExecOptions{
		Args: []any{articleID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			targetEntityID = int(stmt.ColumnInt64(0))
			return nil
		},
	})

	if targetEntityID == 0 {
		return ArticleResult{}, fmt.Errorf("article not found")
	}

	res, err := h.ArticleGetByEntity(targetEntityID)
	if err != nil {
		return res, err
	}
	res.ID = articleID
	return res, nil
}

func (h *DBHandler) ArticleGet(entityOrArticleID int) (ArticleResult, error) {
	res, err := h.ArticleGetByEntity(entityOrArticleID)
	if err == nil && len(res.Sections) > 0 {
		return res, nil
	}
	return h.ArticleGetByArticle(entityOrArticleID)
}
