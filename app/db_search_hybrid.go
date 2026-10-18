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
		item     SearchResult
		rrfScore float64
	}

	candMap := make(map[int]*candidate)

	const kRRF = 60.0

	mergeList := func(results []SearchResult) {
		for rank, res := range results {
			if res.EntityID == 0 {
				continue
			}
			scoreBonus := 1.0 / (kRRF + float64(rank+1))

			if existing, ok := candMap[res.EntityID]; ok {
				existing.rrfScore += scoreBonus

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
					item:     res,
					rrfScore: scoreBonus,
				}
			}
		}
	}

	mergeList(entResults)
	mergeList(lexResults)
	mergeList(semResults)

	merged := make([]*candidate, 0, len(candMap))
	for _, c := range candMap {
		merged = append(merged, c)
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].rrfScore > merged[j].rrfScore
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}

	maxPossibleRRF := 0.0
	numActiveLists := 0
	if len(entResults) > 0 {
		numActiveLists++
	}
	if len(lexResults) > 0 {
		numActiveLists++
	}
	if len(semResults) > 0 {
		numActiveLists++
	}
	if numActiveLists > 0 {
		maxPossibleRRF = float64(numActiveLists) / (kRRF + 1.0)
	}

	results := make([]SearchResult, len(merged))
	for i, c := range merged {
		if maxPossibleRRF > 0 {
			c.item.Power = (c.rrfScore / maxPossibleRRF) * 100.0
		}
		results[i] = c.item
	}

	if len(results) == 0 && lastErr != nil {
		return []SearchResult{}, lastErr
	}
	return results, nil
}
