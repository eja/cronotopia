// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) SearchVectors(query string, limit int, searchParams ...SearchParams) ([]SearchResult, error) {
	conn := h.pool.Get(context.Background())
	if conn == nil {
		return []SearchResult{}, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	var p SearchParams
	if len(searchParams) > 0 {
		p = searchParams[0]
	}
	hasSpace := p.Lat != 0 || p.Lon != 0
	hasTime := p.Year != 0 || p.Month != 0 || p.Day != 0
	if p.RadiusKm <= 0 {
		p.RadiusKm = 100.0
	}

	var countVectors int
	_ = sqlitex.Execute(conn, "SELECT COUNT(*) FROM vectors", &sqlitex.ExecOptions{
		ResultFunc: func(s *sqlite.Stmt) error { countVectors = int(s.ColumnInt64(0)); return nil },
	})

	if !h.AiHasANN() && countVectors == 0 {
		return []SearchResult{}, nil
	}

	fullQuery := options.aiModelPrefixSearch + query
	queryEmbedding, err := aiEmbeddings(fullQuery)
	if err != nil {
		log.Printf("SearchVectors: aiEmbeddings error: %v", err)
		return []SearchResult{}, err
	}

	type candItem struct {
		id  int64
		sim float32
	}
	var topResults []candItem

	if hasSpace || hasTime {
		var whereSub []string
		var filterArgs []any

		if hasSpace {
			minLat, maxLat, minLon, maxLon := BoundingBox(p.Lat, p.Lon, p.RadiusKm)
			whereSub = append(whereSub, `s.entity_id IN (
				SELECT p.entity_id FROM entity_places_rtree r
				JOIN entity_places p ON r.id = p.id
				WHERE r.max_lat >= ? AND r.min_lat <= ? AND r.max_lon >= ? AND r.min_lon <= ?
			)`)
			filterArgs = append(filterArgs, minLat, maxLat, minLon, maxLon)
		}

		if hasTime {
			if p.Year != 0 {
				minJD, maxJD := getJulianDayBounds(p.Year, p.Month, p.Day)
				if p.Range == 0 {
					if minJD == maxJD {
						whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day = ?)`)
						filterArgs = append(filterArgs, minJD)
					} else {
						whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day >= ? AND tm.julian_day <= ?)`)
						filterArgs = append(filterArgs, minJD, maxJD)
					}
				} else if p.Range < 0 {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day <= ?)`)
					filterArgs = append(filterArgs, minJD)
				} else if p.Range > 0 {
					whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.julian_day >= ?)`)
					filterArgs = append(filterArgs, maxJD)
				}
			} else if p.Month > 0 && p.Day > 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.month = ? AND tm.day = ?)`)
				filterArgs = append(filterArgs, p.Month, p.Day)
			} else if p.Month > 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.month = ?)`)
				filterArgs = append(filterArgs, p.Month)
			} else if p.Day > 0 {
				whereSub = append(whereSub, `s.entity_id IN (SELECT tm.entity_id FROM entity_times tm WHERE tm.day = ?)`)
				filterArgs = append(filterArgs, p.Day)
			}
		}

		preFilterSQL := fmt.Sprintf(`
			SELECT v.id, v.embedding 
			FROM vectors v
			JOIN sections s ON v.id = s.id
			WHERE %s
		`, strings.Join(whereSub, " AND "))

		_ = sqlitex.Execute(conn, preFilterSQL, &sqlitex.ExecOptions{
			Args: filterArgs,
			ResultFunc: func(stmt *sqlite.Stmt) error {
				id := stmt.ColumnInt64(0)
				bLen := stmt.ColumnLen(1)
				buf := make([]byte, bLen)
				stmt.ColumnBytes(1, buf)
				fBuf := BytesToFloat32(buf)
				if len(fBuf) == len(queryEmbedding) {
					sim := dot(queryEmbedding, fBuf)
					topResults = append(topResults, candItem{id: id, sim: sim})
				}
				return nil
			},
		})
	} else {
		candidateLimit := limit * 4
		if candidateLimit < 50 {
			candidateLimit = 50
		}

		if h.AiHasANN() {
			topAnnResults, err := h.SearchAnn(queryEmbedding, options.aiAnnSize, candidateLimit)
			if err != nil {
				log.Printf("SearchVectors: SearchAnn error: %v", err)
				return []SearchResult{}, err
			}

			var ids []string
			for _, v := range topAnnResults {
				var vectorID int64
				_ = sqlitex.Execute(conn, "SELECT vectors_id FROM vectors_ann_index WHERE chunk_id = ? AND chunk_position = ? LIMIT 1", &sqlitex.ExecOptions{
					Args: []any{v.ChunkRowID, v.ChunkPosition},
					ResultFunc: func(stmt *sqlite.Stmt) error {
						vectorID = stmt.ColumnInt64(0)
						return nil
					},
				})
				if vectorID > 0 {
					topResults = append(topResults, candItem{id: vectorID, sim: v.Distance})
					ids = append(ids, strconv.FormatInt(vectorID, 10))
				}
			}

			if countVectors > 0 && len(ids) > 0 {
				rerankMap := make(map[int64]float32)
				sqlQuery := "SELECT id, embedding FROM vectors WHERE id IN (" + strings.Join(ids, ",") + ")"
				_ = sqlitex.Execute(conn, sqlQuery, &sqlitex.ExecOptions{
					ResultFunc: func(stmt *sqlite.Stmt) error {
						id := stmt.ColumnInt64(0)
						bLen := stmt.ColumnLen(1)
						buf := make([]byte, bLen)
						stmt.ColumnBytes(1, buf)
						fBuf := BytesToFloat32(buf)
						if len(fBuf) == len(queryEmbedding) {
							rerankMap[id] = dot(queryEmbedding, fBuf)
						}
						return nil
					},
				})
				for i := range topResults {
					if fullSim, ok := rerankMap[topResults[i].id]; ok {
						topResults[i].sim = fullSim
					}
				}
			}
		} else if countVectors > 0 {
			_ = sqlitex.Execute(conn, "SELECT id, embedding FROM vectors", &sqlitex.ExecOptions{
				ResultFunc: func(stmt *sqlite.Stmt) error {
					id := stmt.ColumnInt64(0)
					bLen := stmt.ColumnLen(1)
					buf := make([]byte, bLen)
					stmt.ColumnBytes(1, buf)
					fBuf := BytesToFloat32(buf)
					if len(fBuf) == len(queryEmbedding) {
						sim := dot(queryEmbedding, fBuf)
						topResults = append(topResults, candItem{id: id, sim: sim})
					}
					return nil
				},
			})
		}
	}

	sort.Slice(topResults, func(i, j int) bool {
		return topResults[i].sim > topResults[j].sim
	})

	results := make([]SearchResult, 0, limit)
	seenEntity := make(map[int]bool)

	for _, vd := range topResults {
		var res SearchResult
		query := `
			SELECT s.entity_id, COALESCE(e.article_title, s.title), s.content, COALESCE(e.article_id, 0)
			FROM sections s
			LEFT JOIN entities e ON s.entity_id = e.id
			WHERE s.id = ?
		`
		_ = sqlitex.Execute(conn, query, &sqlitex.ExecOptions{
			Args: []any{vd.id},
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

		info := h.GetEntityInfo(conn, res.EntityID, p.Lat, p.Lon)
		matchCode := 0
		if hasSpace {
			if !info.HasCoord || HaversineDistance(p.Lat, p.Lon, info.Lat, info.Lon) > p.RadiusKm {
				continue
			}
			matchCode = info.Code
		}
		if hasTime {
			matched, code := h.entityMatchesTime(conn, res.EntityID, p)
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
		res.Type = "V"
		res.Snippet = Snippet(res.Text, 160)
		res.Power = math.Max(0, math.Min(100, float64((vd.sim+1.0)/2.0*100.0)))
		results = append(results, res)

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}
