// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package acta

import (
	"cronotopia/corpus"
	"cronotopia/media"
	"zombiezen.com/go/sqlite"
)

type ArticleResult struct {
	ID        int                           `json:"id"`
	Title     string                        `json:"title,omitempty"`
	EntityID  int                           `json:"entity_id,omitempty"`
	Sections  []corpus.ArticleResultSection `json:"sections,omitempty"`
	Latitude  float64                       `json:"latitude,omitempty"`
	Longitude float64                       `json:"longitude,omitempty"`
	Year      int                           `json:"year,omitempty"`
	DateBegin string                        `json:"date_begin,omitempty"`
	DateEnd   string                        `json:"date_end,omitempty"`
	Media     []media.Item                  `json:"media,omitempty"`
}

func GetArticleByEntity(conn *sqlite.Conn, entityID int) (ArticleResult, error) {
	raw, err := corpus.GetArticleByEntity(conn, entityID)
	if err != nil {
		return ArticleResult{}, err
	}

	info := GetEntityInfo(conn, raw.EntityID, 0, 0)
	return ArticleResult{
		ID:        raw.ID,
		Title:     raw.Title,
		EntityID:  raw.EntityID,
		Sections:  raw.Sections,
		Latitude:  info.Lat,
		Longitude: info.Lon,
		Year:      info.YearBegin,
		DateBegin: info.DateBegin,
		DateEnd:   info.DateEnd,
		Media:     info.Media,
	}, nil
}

func GetArticle(conn *sqlite.Conn, articleID int) (ArticleResult, error) {
	raw, err := corpus.GetArticle(conn, articleID)
	if err != nil {
		return ArticleResult{}, err
	}

	info := GetEntityInfo(conn, raw.EntityID, 0, 0)
	return ArticleResult{
		ID:        raw.ID,
		Title:     raw.Title,
		EntityID:  raw.EntityID,
		Sections:  raw.Sections,
		Latitude:  info.Lat,
		Longitude: info.Lon,
		Year:      info.YearBegin,
		DateBegin: info.DateBegin,
		DateEnd:   info.DateEnd,
		Media:     info.Media,
	}, nil
}
