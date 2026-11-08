// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func (h *DBHandler) GetTileData(z, x, y int) ([]byte, error) {
	if z < 0 || z > 30 {
		return nil, fmt.Errorf("zoom level out of bounds: %d", z)
	}

	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	yTms := (1 << z) - 1 - y
	var tileData []byte
	err := sqlitex.Execute(conn, "SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?", &sqlitex.ExecOptions{
		Args: []any{z, x, yTms},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			tileData = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, tileData)
			return nil
		},
	})
	return tileData, err
}

func (h *DBHandler) GetElevationTileData(z, x, y int) ([]byte, error) {
	if z < 0 || z > 30 {
		return nil, fmt.Errorf("zoom level out of bounds: %d", z)
	}

	conn := h.pool.Get(context.Background())
	if conn == nil {
		return nil, fmt.Errorf("failed to get connection")
	}
	defer h.pool.Put(conn)

	yTms := (1 << z) - 1 - y
	var tileData []byte
	err := sqlitex.Execute(conn, "SELECT tile_data FROM elevations WHERE zoom_level=? AND tile_column=? AND tile_row=?", &sqlitex.ExecOptions{
		Args: []any{z, x, yTms},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			tileData = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, tileData)
			return nil
		},
	})
	return tileData, err
}

func handleTiles(w http.ResponseWriter, r *http.Request) {
	cleanPath := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(cleanPath, "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}

	isElevation := len(parts) >= 4 && parts[1] == "elevations"

	z, errZ := strconv.Atoi(parts[len(parts)-3])
	x, errX := strconv.Atoi(parts[len(parts)-2])
	yStr := parts[len(parts)-1]
	yStr = strings.TrimSuffix(yStr, ".pbf")
	yStr = strings.TrimSuffix(yStr, ".png")
	y, errY := strconv.Atoi(yStr)

	if errZ != nil || errX != nil || errY != nil || z < 0 || z > 30 {
		http.NotFound(w, r)
		return
	}

	var data []byte
	var err error

	if isElevation {
		data, err = db.GetElevationTileData(z, x, y)
		if err != nil || len(data) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write(data)
		return
	}

	data, err = db.GetTileData(z, x, y)
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(data)
}

func MapHaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * (math.Pi / 180.0)
	dLon := (lon2 - lon1) * (math.Pi / 180.0)

	rLat1 := lat1 * (math.Pi / 180.0)
	rLat2 := lat2 * (math.Pi / 180.0)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Sin(dLon/2)*math.Sin(dLon/2)*math.Cos(rLat1)*math.Cos(rLat2)

	if a > 1.0 {
		a = 1.0
	} else if a < 0.0 {
		a = 0.0
	}

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}

func MapBoundingBox(lat, lon, radiusKm float64) (minLat, maxLat, minLon, maxLon float64) {
	const earthRadiusKm = 6371.0
	radDist := radiusKm / earthRadiusKm

	degLat := radDist * (180.0 / math.Pi)
	minLat = lat - degLat
	maxLat = lat + degLat

	if minLat <= -90.0 || maxLat >= 90.0 || radDist >= math.Pi/2 {
		minLat = math.Max(-90.0, minLat)
		maxLat = math.Min(90.0, maxLat)
		return minLat, maxLat, -180.0, 180.0
	}

	radLat := lat * (math.Pi / 180.0)
	cosLat := math.Cos(radLat)
	sinDist := math.Sin(radDist)

	if cosLat == 0 || sinDist >= cosLat {
		minLon = -180.0
		maxLon = 180.0
	} else {
		deltaLonRad := math.Asin(sinDist / cosLat)
		deltaLonDeg := deltaLonRad * (180.0 / math.Pi)
		minLon = lon - deltaLonDeg
		maxLon = lon + deltaLonDeg
		if minLon < -180.0 || maxLon > 180.0 {
			minLon = -180.0
			maxLon = 180.0
		}
	}
	return
}

func MapLatLonToTile(lat, lon float64, zoom int) (int, int) {
	if lat > 85.05112878 {
		lat = 85.05112878
	} else if lat < -85.05112878 {
		lat = -85.05112878
	}
	n := math.Pow(2, float64(zoom))
	x := int(math.Floor((lon + 180.0) / 360.0 * n))
	latRad := lat * math.Pi / 180.0
	y := int(math.Floor((1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n))

	maxTile := (1 << zoom) - 1
	if x < 0 {
		x = 0
	} else if x > maxTile {
		x = maxTile
	}
	if y < 0 {
		y = 0
	} else if y > maxTile {
		y = maxTile
	}
	return x, y
}

func MapTileXYToLatLon(tx, ty, z int, px, py int64, extent float64) [2]float64 {
	zExp := math.Pow(2, float64(z))
	wX := (float64(tx) + float64(px)/extent) / zExp
	wY := (float64(ty) + float64(py)/extent) / zExp
	lng := wX*360.0 - 180.0
	latRad := math.Atan(math.Sinh(math.Pi * (1.0 - 2.0*wY)))
	lat := latRad * 180.0 / math.Pi
	return [2]float64{lng, lat}
}

type mapPBReader struct {
	buf []byte
	pos int
}

func (p *mapPBReader) hasMore() bool {
	return p.pos < len(p.buf)
}

func (p *mapPBReader) readVarint() uint64 {
	v, n := binary.Uvarint(p.buf[p.pos:])
	p.pos += n
	return v
}

func (p *mapPBReader) readBytes() []byte {
	l := int(p.readVarint())
	data := p.buf[p.pos : p.pos+l]
	p.pos += l
	return data
}

func (p *mapPBReader) skip(wireType int) {
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

func MapDecodeMVT(data []byte, tileX, tileY, zoom int) ([]MapTileFeature, error) {
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

	var features []MapTileFeature
	tilePB := mapPBReader{buf: data}

	for tilePB.hasMore() {
		tag := tilePB.readVarint()
		fieldNum, wire := tag>>3, int(tag&7)
		if fieldNum == 3 && wire == 2 {
			layerBytes := tilePB.readBytes()
			features = append(features, mapParseLayer(layerBytes, tileX, tileY, zoom)...)
		} else {
			tilePB.skip(wire)
		}
	}
	return features, nil
}

func mapParseLayer(buf []byte, tileX, tileY, zoom int) []MapTileFeature {
	r := mapPBReader{buf: buf}
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
			values = append(values, mapParseValue(r.readBytes()))
		case 5:
			extent = float64(r.readVarint())
		default:
			r.skip(wire)
		}
	}

	out := make([]MapTileFeature, 0, len(rawFeatures))
	for _, featBuf := range rawFeatures {
		feat := mapParseFeature(featBuf, layerName, keys, values, extent, tileX, tileY, zoom)
		out = append(out, feat)
	}
	return out
}

func mapParseValue(buf []byte) any {
	r := mapPBReader{buf: buf}
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
			return mapZigzag(n)
		case 7:
			return r.readVarint() != 0
		default:
			r.skip(wire)
		}
	}
	return nil
}

func mapGenerateCompositeID(rawID uint64, layer string, props map[string]any) string {
	parts := []string{strconv.FormatUint(rawID, 10), layer}
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
	return strings.Join(parts, ":")
}

func mapParseFeature(buf []byte, layer string, keys []string, values []any, extent float64, tileX, tileY, zoom int) MapTileFeature {
	r := mapPBReader{buf: buf}
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
			pr := mapPBReader{buf: b}
			for pr.hasMore() {
				tags = append(tags, int(pr.readVarint()))
			}
		case 3:
			geomType = int(r.readVarint())
		case 4:
			b := r.readBytes()
			pr := mapPBReader{buf: b}
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
		featID = mapResolveIDFromProperties(props)
	}

	coords, typeStr := mapDecodeGeometry(geomCmds, geomType, extent, tileX, tileY, zoom)
	compositeID := mapGenerateCompositeID(featID, layer, props)

	return MapTileFeature{
		ID:         compositeID,
		RawID:      featID,
		Layer:      layer,
		Type:       typeStr,
		Properties: props,
		Geometry:   coords,
	}
}

func mapResolveIDFromProperties(props map[string]any) uint64 {
	for _, key := range []string{"osm_id", "id", "@id"} {
		if val, exists := props[key]; exists {
			switch v := val.(type) {
			case uint64:
				if v != 0 {
					return v
				}
			case int64:
				if v > 0 {
					return uint64(v)
				}
			case float64:
				if v > 0 {
					return uint64(v)
				}
			case string:
				if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed != 0 {
					return parsed
				}
			}
		}
	}
	return 0
}

func mapZigzag(n uint32) int64 {
	return int64(n>>1) ^ -int64(n&1)
}

func mapDecodeGeometry(cmds []uint32, gType int, extent float64, tx, ty, z int) ([][][2]float64, string) {
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
				curX += mapZigzag(cmds[idx])
				curY += mapZigzag(cmds[idx+1])
				idx += 2
				if i == 0 {
					startX = curX
					startY = curY
				}
				currentRing = append(currentRing, MapTileXYToLatLon(tx, ty, z, curX, curY, extent))
			}
		case 2:
			for i := 0; i < count; i++ {
				curX += mapZigzag(cmds[idx])
				curY += mapZigzag(cmds[idx+1])
				idx += 2
				currentRing = append(currentRing, MapTileXYToLatLon(tx, ty, z, curX, curY, extent))
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
