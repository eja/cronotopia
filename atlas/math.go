// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package atlas

import (
	"math"
)

func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
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

func BoundingBox(lat, lon, radiusKm float64) (minLat, maxLat, minLon, maxLon float64) {
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

func LatLonToTile(lat, lon float64, zoom int) (int, int) {
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

func TileXYToLatLon(tx, ty, z int, px, py int64, extent float64) [2]float64 {
	zExp := math.Pow(2, float64(z))
	wX := (float64(tx) + float64(px)/extent) / zExp
	wY := (float64(ty) + float64(py)/extent) / zExp
	lng := wX*360.0 - 180.0
	latRad := math.Atan(math.Sinh(math.Pi * (1.0 - 2.0*wY)))
	lat := latRad * 180.0 / math.Pi
	return [2]float64{lng, lat}
}
