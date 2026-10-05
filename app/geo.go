// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import "math"

func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * (math.Pi / 180.0)
	dLon := (lon2 - lon1) * (math.Pi / 180.0)

	rLat1 := lat1 * (math.Pi / 180.0)
	rLat2 := lat2 * (math.Pi / 180.0)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Sin(dLon/2)*math.Sin(dLon/2)*math.Cos(rLat1)*math.Cos(rLat2)
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
