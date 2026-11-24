// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package corpus

import (
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"

	"cronotopia/acta"
	"cronotopia/atlas"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func SearchLexical(conn *sqlite.Conn, searchQuery string, limit int, searchParams ...acta.SearchParams) ([]acta.SearchResult, error) {
	sanitized := acta.SanitizeFTSQuery(searchQuery)
	if sanitized == "" {
		return nil, nil
	}

	ftsSchema := acta.TableSchema(conn, "section_search")
	if !acta.HasSchemaTable(conn, ftsSchema, "section_search") {
		return nil, nil
	}

	secSchema := acta.TableSchema(conn, "sections")
	entSchema := acta.TableSchema(conn, "entities")

	var p acta.SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}

	hasSpace := p.HasSpace || (p.Lat != 0 || p.Lon != 0)
	hasTime := p.HasTime || (p.Year != 0 || p.Month != 0 || p.Day != 0)
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}

	fetchLimit := limit * 6
	if fetchLimit < 60 {
		fetchLimit = 60
	}
	if hasSpace || hasTime {
		fetchLimit = acta.MaxSearchCandidates
	}

	sqlQuery := fmt.Sprintf(`
		SELECT
			s.entity_id,
			COALESCE(e.title, s.title),
			COALESCE(e.article_id, 0),
			s.content,
			snippet(section_search, 1, '<mark>', '</mark>', '...', 32) as snippet,
			bm25(section_search) as power
		FROM %s.section_search
		JOIN %s.sections s ON section_search.rowid = s.id
		LEFT JOIN %s.entities e ON s.entity_id = e.id
		WHERE section_search MATCH ?
		ORDER BY power ASC
		LIMIT ?
	`, ftsSchema, secSchema, entSchema)

	var results []acta.SearchResult
	seenEntity := make(map[int]bool)

	err := sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
		Args: []any{sanitized, fetchLimit},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			if len(results) >= limit {
				return acta.ErrDone
			}

			entID := int(stmt.ColumnInt64(0))
			if seenEntity[entID] {
				return nil
			}

			info := acta.GetEntityInfo(conn, entID, p.Lat, p.Lon)
			matchCode := 0
			if hasSpace {
				if !info.HasCoord || atlas.HaversineDistance(p.Lat, p.Lon, info.Lat, info.Lon) > p.RadiusKm {
					return nil
				}
				matchCode = info.Code
			}
			if hasTime {
				matched, code := acta.MatchesTime(conn, entID, p)
				if !matched {
					return nil
				}
				matchCode = code
			}

			seenEntity[entID] = true
			res := acta.SearchResult{
				EntityID:  entID,
				ArticleID: int(stmt.ColumnInt64(2)),
				Title:     stmt.ColumnText(1),
				Text:      stmt.ColumnText(3),
				Snippet:   stmt.ColumnText(4),
				Power:     acta.NormalizeBM25(stmt.ColumnFloat(5)),
				Lat:       info.Lat,
				Lon:       info.Lon,
				Year:      info.YearBegin,
				Code:      matchCode,
				DateBegin: info.DateBegin,
				DateEnd:   info.DateEnd,
				Times:     info.Times,
				Places:    info.Places,
				Media:     info.Media,
				Type:      "L",
			}

			if res.Snippet == "" {
				res.Snippet = acta.Snippet(res.Text, 160)
			}
			results = append(results, res)

			if len(results) >= limit {
				return acta.ErrDone
			}
			return nil
		},
	})

	if err != nil && !errors.Is(err, acta.ErrDone) {
		return nil, err
	}
	return results, nil
}

func SearchVectors(conn *sqlite.Conn, eng *Engine, query string, limit int, searchParams ...acta.SearchParams) ([]acta.SearchResult, error) {
	if eng == nil || !eng.HasANN(conn) {
		return []acta.SearchResult{}, nil
	}

	var p acta.SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}
	hasSpace := p.HasSpace || (p.Lat != 0 || p.Lon != 0)
	hasTime := p.HasTime || (p.Year != 0 || p.Month != 0 || p.Day != 0)
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}
	if limit <= 0 {
		limit = 10
	}

	fullQuery := eng.config.ModelPrefixSearch + query
	queryEmbedding, err := eng.Embeddings(fullQuery)
	if err != nil {
		log.Printf("SearchVectors: embedding error: %v", err)
		return []acta.SearchResult{}, err
	}

	candidateLimit := limit * 6
	if candidateLimit < 60 {
		candidateLimit = 60
	}
	if hasSpace || hasTime {
		candidateLimit = acta.MaxSearchCandidates
	}

	topAnnResults, err := eng.SearchAnn(conn, queryEmbedding, eng.config.AnnSize, candidateLimit)
	if err != nil {
		log.Printf("SearchVectors: SearchAnn error: %v", err)
		return []acta.SearchResult{}, err
	}

	annIndexSchema := acta.TableSchema(conn, "vectors_ann_index")
	secSchema := acta.TableSchema(conn, "sections")
	entSchema := acta.TableSchema(conn, "entities")

	results := make([]acta.SearchResult, 0, limit)
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

		var res acta.SearchResult
		querySQL := fmt.Sprintf(`
			SELECT s.entity_id, COALESCE(e.title, s.title), s.content, COALESCE(e.article_id, 0)
			FROM %s.sections s
			LEFT JOIN %s.entities e ON s.entity_id = e.id
			WHERE s.id = ?
		`, secSchema, entSchema)
		_ = sqlitex.Execute(conn, querySQL, &sqlitex.ExecOptions{
			Args: []any{vectorID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				res.EntityID = int(stmt.ColumnInt64(0))
				res.Title = stmt.ColumnText(1)
				res.Text = stmt.ColumnText(2)
				res.ArticleID = int(stmt.ColumnInt64(3))
				return nil
			},
		})

		if res.EntityID == 0 || seenEntity[res.EntityID] {
			continue
		}

		info := acta.GetEntityInfo(conn, res.EntityID, p.Lat, p.Lon)
		matchCode := 0
		if hasSpace {
			if !info.HasCoord || atlas.HaversineDistance(p.Lat, p.Lon, info.Lat, info.Lon) > p.RadiusKm {
				continue
			}
			matchCode = info.Code
		}
		if hasTime {
			matched, code := acta.MatchesTime(conn, res.EntityID, p)
			if !matched {
				continue
			}
			matchCode = code
		}

		seenEntity[res.EntityID] = true
		res.Lat = info.Lat
		res.Lon = info.Lon
		res.Year = info.YearBegin
		res.Code = matchCode
		res.DateBegin = info.DateBegin
		res.DateEnd = info.DateEnd
		res.Times = info.Times
		res.Places = info.Places
		res.Media = info.Media
		res.Type = "S"
		res.Snippet = acta.Snippet(res.Text, 160)
		res.Power = math.Max(0, math.Min(100, float64((v.Distance+1.0)/2.0*100.0)))
		results = append(results, res)

		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func SearchHybrid(conn *sqlite.Conn, eng *Engine, query string, limit int, searchParams ...acta.SearchParams) ([]acta.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	var p acta.SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}

	var lastErr error
	entResults, err := acta.SearchEntityLabels(conn, query, limit, p)
	if err != nil {
		lastErr = err
	}

	lexResults, err := SearchLexical(conn, query, limit, p)
	if err != nil {
		lastErr = err
	}

	var semResults []acta.SearchResult
	if eng != nil {
		semResults, err = SearchVectors(conn, eng, query, limit, p)
		if err != nil {
			lastErr = err
		}
	}

	type candidate struct {
		item     acta.SearchResult
		rrfScore float64
	}

	candMap := make(map[int]*candidate)
	const kRRF = 60.0

	mergeList := func(results []acta.SearchResult) {
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
				if len(existing.item.Media) == 0 && len(res.Media) > 0 {
					existing.item.Media = res.Media
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

	results := make([]acta.SearchResult, len(merged))
	for i, c := range merged {
		if maxPossibleRRF > 0 {
			c.item.Power = (c.rrfScore / maxPossibleRRF) * 100.0
		}
		results[i] = c.item
	}

	if len(results) == 0 && lastErr != nil {
		return []acta.SearchResult{}, lastErr
	}
	return results, nil
}
