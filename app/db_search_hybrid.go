// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"sort"
	"strings"
)

func (h *DBHandler) SearchHybrid(query string, limit int, searchParams ...SearchParams) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	var p SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}

	var lastErr error
	entResults, err := h.SearchEntityLabels(query, limit, p)
	if err != nil {
		lastErr = err
	}

	lexResults, err := h.SearchLexical(query, limit, p)
	if err != nil {
		lastErr = err
	}

	var semResults []SearchResult
	if ai {
		semResults, err = h.SearchVectors(query, limit, p)
		if err != nil {
			lastErr = err
		}
	}

	type candidate struct {
		item       SearchResult
		matchCount int
	}

	candMap := make(map[int]*candidate)

	addResult := func(res SearchResult) {
		if res.EntityID == 0 {
			return
		}
		if existing, ok := candMap[res.EntityID]; ok {
			existing.matchCount++
			if res.Power > existing.item.Power {
				existing.item.Power = res.Power
			}
			if strings.Contains(res.Snippet, "<mark>") || (len(res.Snippet) > len(existing.item.Snippet) && !strings.Contains(existing.item.Snippet, "<mark>")) {
				existing.item.Snippet = res.Snippet
			}
			if existing.item.Text == "" && res.Text != "" {
				existing.item.Text = res.Text
			}
			if existing.item.Lat == 0 && res.Lat != 0 {
				existing.item.Lat = res.Lat
				existing.item.Lon = res.Lon
			}
			if existing.item.Year == 0 && res.Year != 0 {
				existing.item.Year = res.Year
			}
			if existing.item.DateBegin == "" && res.DateBegin != "" {
				existing.item.DateBegin = res.DateBegin
				existing.item.DateEnd = res.DateEnd
			}
			if len(existing.item.Times) == 0 && len(res.Times) > 0 {
				existing.item.Times = res.Times
			}
			if len(existing.item.Places) == 0 && len(res.Places) > 0 {
				existing.item.Places = res.Places
			}
			if existing.item.ArticleID == 0 && res.ArticleID != 0 {
				existing.item.ArticleID = res.ArticleID
			}
			existing.item.Type = "H"
		} else {
			candMap[res.EntityID] = &candidate{
				item:       res,
				matchCount: 1,
			}
		}
	}

	for _, r := range entResults {
		addResult(r)
	}
	for _, r := range lexResults {
		addResult(r)
	}
	for _, r := range semResults {
		addResult(r)
	}

	merged := make([]*candidate, 0, len(candMap))
	for _, c := range candMap {
		merged = append(merged, c)
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].matchCount != merged[j].matchCount {
			return merged[i].matchCount > merged[j].matchCount
		}
		return merged[i].item.Power > merged[j].item.Power
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}

	results := make([]SearchResult, len(merged))
	for i, c := range merged {
		results[i] = c.item
	}

	if len(results) == 0 && lastErr != nil {
		return []SearchResult{}, lastErr
	}
	return results, nil
}
