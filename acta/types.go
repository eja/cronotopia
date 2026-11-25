// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package acta

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"cronotopia/media"
)

const MaxSearchCandidates = 2000

var ErrDone = errors.New("search limit reached")

type SearchParams struct {
	QueryText        string
	Day, Month, Year int
	Lat, Lon         float64
	RadiusKm         float64
	HasSpace         bool
	HasTime          bool
	Range            int
	Limit            int
}

type EventTime struct {
	Code  int    `json:"code"`
	Year  int    `json:"year"`
	Month int    `json:"month,omitempty"`
	Day   int    `json:"day,omitempty"`
	Date  string `json:"date,omitempty"`
}

type EventPlace struct {
	Code      int     `json:"code"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type EventResult struct {
	ID          int          `json:"id"`
	ArticleID   int          `json:"article_id,omitempty"`
	Code        int          `json:"code"`
	Lat         float64      `json:"latitude"`
	Lon         float64      `json:"longitude"`
	Day         int          `json:"day"`
	Month       int          `json:"month"`
	Year        int          `json:"year"`
	JulianDay   int64        `json:"julian_day,omitempty"`
	Label       string       `json:"label"`
	Description string       `json:"description,omitempty"`
	DistanceKm  float64      `json:"distance_km,omitempty"`
	TimeDiff    int64        `json:"time_diff,omitempty"`
	DateBegin   string       `json:"date_begin,omitempty"`
	DateEnd     string       `json:"date_end,omitempty"`
	Times       []EventTime  `json:"times,omitempty"`
	Places      []EventPlace `json:"places,omitempty"`
	Media       []media.Item `json:"media,omitempty"`
}

type SearchResult struct {
	ArticleID int          `json:"article_id,omitempty"`
	EntityID  int          `json:"entity_id,omitempty"`
	Title     string       `json:"title,omitempty"`
	Text      string       `json:"text"`
	Type      string       `json:"type,omitempty"`
	Power     float64      `json:"power"`
	Snippet   string       `json:"snippet"`
	Lat       float64      `json:"latitude,omitempty"`
	Lon       float64      `json:"longitude,omitempty"`
	Year      int          `json:"year,omitempty"`
	Code      int          `json:"code,omitempty"`
	DateBegin string       `json:"date_begin,omitempty"`
	DateEnd   string       `json:"date_end,omitempty"`
	Times     []EventTime  `json:"times,omitempty"`
	Places    []EventPlace `json:"places,omitempty"`
	Media     []media.Item `json:"media,omitempty"`
}

type EntityInfo struct {
	Lat       float64
	Lon       float64
	HasCoord  bool
	Code      int
	DateBegin string
	DateEnd   string
	YearBegin int
	YearEnd   int
	Times     []EventTime
	Places    []EventPlace
	Media     []media.Item
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

func SanitizeFTSQuery(query string) string {
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
