// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package atlas

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
)

type TileFeature struct {
	ID         string         `json:"id"`
	RawID      uint64         `json:"-"`
	Layer      string         `json:"layer"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   [][][2]float64 `json:"coordinates"`
}

type TileCentroid struct {
	ID         string         `json:"id"`
	Layer      string         `json:"layer"`
	Type       string         `json:"type"`
	Height     *float64       `json:"height,omitempty"`
	Elevation  *float64       `json:"elevation,omitempty"`
	Properties map[string]any `json:"properties"`
	Center     [2]float64     `json:"center"`
	BBox       [4]float64     `json:"bbox"`
}

type pbReader struct {
	buf []byte
	pos int
}

func (p *pbReader) hasMore() bool {
	return p.pos < len(p.buf)
}

func (p *pbReader) readVarint() uint64 {
	v, n := binary.Uvarint(p.buf[p.pos:])
	p.pos += n
	return v
}

func (p *pbReader) readBytes() []byte {
	l := int(p.readVarint())
	data := p.buf[p.pos : p.pos+l]
	p.pos += l
	return data
}

func (p *pbReader) skip(wireType int) {
	switch wireType {
	case 0:
		_ = p.readVarint()
	case 1:
		p.pos += 8
	case 2:
		l := int(p.readVarint())
		p.pos += l
	case 5:
		p.pos += 4
	}
}

func zigzag(n uint32) int64 {
	return int64(n>>1) ^ -int64(n&1)
}

func DecodeMVT(data []byte, tileX, tileY, zoom int) ([]TileFeature, error) {
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		var errRead error
		data, errRead = io.ReadAll(gz)
		if errRead != nil {
			return nil, errRead
		}
	}

	var features []TileFeature
	tilePB := pbReader{buf: data}

	for tilePB.hasMore() {
		tag := tilePB.readVarint()
		fieldNum, wire := tag>>3, int(tag&7)
		if fieldNum == 3 && wire == 2 {
			layerBytes := tilePB.readBytes()
			features = append(features, parseLayer(layerBytes, tileX, tileY, zoom)...)
		} else {
			tilePB.skip(wire)
		}
	}
	return features, nil
}

func parseLayer(buf []byte, tileX, tileY, zoom int) []TileFeature {
	r := pbReader{buf: buf}
	var layerName string
	extent := 4096.0
	var keys []string
	var values []any
	var rawFeatures [][]byte

	for r.hasMore() {
		tag := r.readVarint()
		fieldNum, wire := tag>>3, int(tag&7)
		switch fieldNum {
		case 1:
			layerName = string(r.readBytes())
		case 2:
			rawFeatures = append(rawFeatures, r.readBytes())
		case 3:
			keys = append(keys, string(r.readBytes()))
		case 4:
			values = append(values, parseValue(r.readBytes()))
		case 5:
			extent = float64(r.readVarint())
		default:
			r.skip(wire)
		}
	}

	out := make([]TileFeature, 0, len(rawFeatures))
	for _, featBuf := range rawFeatures {
		feat := parseFeature(featBuf, layerName, keys, values, extent, tileX, tileY, zoom)
		out = append(out, feat)
	}
	return out
}

func parseValue(buf []byte) any {
	r := pbReader{buf: buf}
	for r.hasMore() {
		tag := r.readVarint()
		fieldNum, wire := tag>>3, int(tag&7)
		switch fieldNum {
		case 1:
			return string(r.readBytes())
		case 2:
			return math.Float32frombits(uint32(r.readVarint()))
		case 3:
			return math.Float64frombits(r.readVarint())
		case 4:
			return int64(r.readVarint())
		case 5:
			return r.readVarint()
		case 6:
			n := uint32(r.readVarint())
			return zigzag(n)
		case 7:
			return r.readVarint() != 0
		default:
			r.skip(wire)
		}
	}
	return nil
}

func parseFeature(buf []byte, layer string, keys []string, values []any, extent float64, tileX, tileY, zoom int) TileFeature {
	r := pbReader{buf: buf}
	var featID uint64
	var geomType int
	var tags []int
	var geomCmds []uint32

	for r.hasMore() {
		tag := r.readVarint()
		fieldNum, wire := tag>>3, int(tag&7)
		switch fieldNum {
		case 1:
			featID = r.readVarint()
		case 2:
			b := r.readBytes()
			pr := pbReader{buf: b}
			for pr.hasMore() {
				tags = append(tags, int(pr.readVarint()))
			}
		case 3:
			geomType = int(r.readVarint())
		case 4:
			b := r.readBytes()
			pr := pbReader{buf: b}
			for pr.hasMore() {
				geomCmds = append(geomCmds, uint32(pr.readVarint()))
			}
		default:
			r.skip(wire)
		}
	}

	props := make(map[string]any)
	for i := 0; i+1 < len(tags); i += 2 {
		kIdx, vIdx := tags[i], tags[i+1]
		if kIdx < len(keys) && vIdx < len(values) {
			props[keys[kIdx]] = values[vIdx]
		}
	}

	if featID == 0 {
		for _, key := range []string{"osm_id", "id", "@id"} {
			if val, exists := props[key]; exists {
				switch v := val.(type) {
				case uint64:
					if v != 0 {
						featID = v
					}
				case int64:
					if v > 0 {
						featID = uint64(v)
					}
				case float64:
					if v > 0 {
						featID = uint64(v)
					}
				case string:
					if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed != 0 {
						featID = parsed
					}
				}
				if featID != 0 {
					break
				}
			}
		}
	}

	coords, typeStr := decodeGeometry(geomCmds, geomType, extent, tileX, tileY, zoom)

	parts := []string{strconv.FormatUint(featID, 10), layer}
	var kind, detail string
	if v, ok := props["kind"].(string); ok && v != "" {
		kind = v
	} else if v, ok := props["class"].(string); ok && v != "" {
		kind = v
	}
	if v, ok := props["kind_detail"].(string); ok && v != "" {
		detail = v
	} else if v, ok := props["subclass"].(string); ok && v != "" {
		detail = v
	}
	if kind != "" || detail != "" {
		parts = append(parts, kind)
		if detail != "" {
			parts = append(parts, detail)
		}
	}

	return TileFeature{
		ID:         strings.Join(parts, ":"),
		RawID:      featID,
		Layer:      layer,
		Type:       typeStr,
		Properties: props,
		Geometry:   coords,
	}
}

func decodeGeometry(cmds []uint32, gType int, extent float64, tx, ty, z int) ([][][2]float64, string) {
	var curX, curY int64
	var startX, startY int64
	var rings [][][2]float64
	var currentRing [][2]float64

	idx := 0
	for idx < len(cmds) {
		cmdInt := cmds[idx]
		idx++
		cmd := cmdInt & 0x7
		count := int(cmdInt >> 3)

		switch cmd {
		case 1:
			if len(currentRing) > 0 {
				rings = append(rings, currentRing)
				currentRing = nil
			}
			for i := 0; i < count; i++ {
				curX += zigzag(cmds[idx])
				curY += zigzag(cmds[idx+1])
				idx += 2
				if i == 0 {
					startX = curX
					startY = curY
				}
				currentRing = append(currentRing, TileXYToLatLon(tx, ty, z, curX, curY, extent))
			}
		case 2:
			for i := 0; i < count; i++ {
				curX += zigzag(cmds[idx])
				curY += zigzag(cmds[idx+1])
				idx += 2
				currentRing = append(currentRing, TileXYToLatLon(tx, ty, z, curX, curY, extent))
			}
		case 7:
			if len(currentRing) > 0 {
				currentRing = append(currentRing, currentRing[0])
				rings = append(rings, currentRing)
				currentRing = nil
				curX = startX
				curY = startY
			}
		}
	}
	if len(currentRing) > 0 {
		rings = append(rings, currentRing)
	}

	typeName := "Unknown"
	switch gType {
	case 1:
		typeName = "Point"
	case 2:
		typeName = "LineString"
	case 3:
		typeName = "Polygon"
	}
	return rings, typeName
}

func CalculateCentroidAndBBox(geometry [][][2]float64) ([2]float64, [4]float64) {
	var sumLon, sumLat float64
	var totalPoints float64
	first := true
	var minLon, maxLon, minLat, maxLat float64

	for _, ring := range geometry {
		for _, pt := range ring {
			lon, lat := pt[0], pt[1]
			if first {
				minLon, maxLon = lon, lon
				minLat, maxLat = lat, lat
				first = false
			} else {
				if lon < minLon {
					minLon = lon
				}
				if lon > maxLon {
					maxLon = lon
				}
				if lat < minLat {
					minLat = lat
				}
				if lat > maxLat {
					maxLat = lat
				}
			}
			sumLon += lon
			sumLat += lat
			totalPoints++
		}
	}

	if totalPoints == 0 {
		return [2]float64{0, 0}, [4]float64{}
	}

	center := [2]float64{sumLon / totalPoints, sumLat / totalPoints}
	bbox := [4]float64{minLon, minLat, maxLon, maxLat}
	return center, bbox
}

func featureWithinRadius(feat TileFeature, centerLat, centerLon, radiusMeters float64) bool {
	radiusKm := radiusMeters / 1000.0
	for _, ring := range feat.Geometry {
		for _, pt := range ring {
			if HaversineDistance(centerLat, centerLon, pt[1], pt[0]) <= radiusKm {
				return true
			}
		}
	}
	return false
}

func ExtractTileFeatures(conn *sqlite.Conn, schema string, centerLat, centerLon, radiusMeters float64, zoom int) ([]TileFeature, error) {
	if zoom < 0 || zoom > 30 {
		return nil, fmt.Errorf("zoom level out of range: %d", zoom)
	}
	if radiusMeters <= 0 {
		radiusMeters = 1000.0
	}

	minLat, maxLat, minLon, maxLon := BoundingBox(centerLat, centerLon, radiusMeters/1000.0)
	x1, y1 := LatLonToTile(maxLat, minLon, zoom)
	x2, y2 := LatLonToTile(minLat, maxLon, zoom)

	minX, maxX := min(x1, x2), max(x1, x2)
	minY, maxY := min(y1, y2), max(y1, y2)

	featureMap := make(map[string]*TileFeature)
	var featureOrder []string

	for tx := minX; tx <= maxX; tx++ {
		for ty := minY; ty <= maxY; ty++ {
			data, err := GetTileData(conn, schema, zoom, tx, ty)
			if err != nil || len(data) == 0 {
				continue
			}
			feats, err := DecodeMVT(data, tx, ty, zoom)
			if err != nil {
				continue
			}
			for _, f := range feats {
				if featureWithinRadius(f, centerLat, centerLon, radiusMeters) {
					if existing, exists := featureMap[f.ID]; exists {
						existing.Geometry = append(existing.Geometry, f.Geometry...)
					} else {
						featCopy := f
						featureMap[f.ID] = &featCopy
						featureOrder = append(featureOrder, f.ID)
					}
				}
			}
		}
	}

	matched := make([]TileFeature, 0, len(featureOrder))
	for _, id := range featureOrder {
		matched = append(matched, *featureMap[id])
	}
	return matched, nil
}

func parseOptionalFloat(val any) *float64 {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case float64:
		return &v
	case float32:
		f := float64(v)
		return &f
	case int:
		f := float64(v)
		return &f
	case int64:
		f := float64(v)
		return &f
	case int32:
		f := float64(v)
		return &f
	case uint:
		f := float64(v)
		return &f
	case uint64:
		f := float64(v)
		return &f
	case uint32:
		f := float64(v)
		return &f
	case string:
		s := strings.TrimSpace(v)
		s = strings.TrimSuffix(s, "m")
		s = strings.TrimSuffix(s, "M")
		s = strings.TrimSpace(s)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return &f
		}
	}
	return nil
}

func ExtractTileCentroids(conn *sqlite.Conn, schema string, centerLat, centerLon, radiusMeters float64, zoom int) ([]TileCentroid, error) {
	features, err := ExtractTileFeatures(conn, schema, centerLat, centerLon, radiusMeters, zoom)
	if err != nil {
		return nil, err
	}

	centroids := make([]TileCentroid, len(features))
	for i, f := range features {
		center, bbox := CalculateCentroidAndBBox(f.Geometry)
		var height *float64
		for _, k := range []string{"height", "render_height"} {
			if v, ok := f.Properties[k]; ok {
				if parsed := parseOptionalFloat(v); parsed != nil {
					height = parsed
					break
				}
			}
		}
		var elevation *float64
		for _, k := range []string{"elevation", "ele"} {
			if v, ok := f.Properties[k]; ok {
				if parsed := parseOptionalFloat(v); parsed != nil {
					elevation = parsed
					break
				}
			}
		}

		centroids[i] = TileCentroid{
			ID:         f.ID,
			Layer:      f.Layer,
			Type:       f.Type,
			Height:     height,
			Elevation:  elevation,
			Properties: f.Properties,
			Center:     center,
			BBox:       bbox,
		}
	}
	return centroids, nil
}

func ExtractFeatureByID(conn *sqlite.Conn, schema string, id string, minLon, minLat, maxLon, maxLat float64, zoom int) (*TileFeature, error) {
	if zoom < 0 || zoom > 30 {
		return nil, fmt.Errorf("zoom level out of range: %d", zoom)
	}

	x1, y1 := LatLonToTile(maxLat, minLon, zoom)
	x2, y2 := LatLonToTile(minLat, maxLon, zoom)

	minX, maxX := min(x1, x2), max(x1, x2)
	minY, maxY := min(y1, y2), max(y1, y2)

	var found *TileFeature
	parsedQueryID, _ := strconv.ParseUint(id, 10, 64)

	for tx := minX; tx <= maxX; tx++ {
		for ty := minY; ty <= maxY; ty++ {
			data, err := GetTileData(conn, schema, zoom, tx, ty)
			if err != nil || len(data) == 0 {
				continue
			}
			feats, err := DecodeMVT(data, tx, ty, zoom)
			if err != nil {
				continue
			}
			for _, f := range feats {
				matched := f.ID == id || (parsedQueryID != 0 && f.RawID == parsedQueryID)
				if matched {
					if found == nil {
						featCopy := f
						found = &featCopy
					} else {
						found.Geometry = append(found.Geometry, f.Geometry...)
					}
				}
			}
		}
	}
	return found, nil
}
