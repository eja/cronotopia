package main

import (
	"encoding/json"
)

const (
	BatchSize     = 10000
	ChannelBuffer = 1000
)

var timeProps = map[int]bool{
	569: true, 570: true, 571: true, 575: true, 576: true,
	577: true, 580: true, 582: true, 585: true, 729: true,
	730: true, 746: true, 1191: true, 1249: true, 1319: true,
	1326: true, 1619: true, 2031: true, 2032: true, 2669: true,
	2754: true, 3999: true, 5204: true, 6949: true, 7124: true,
	7125: true, 7588: true, 7589: true, 9667: true, 10135: true,
}

var placeProps = map[int]bool{625: true}
var linkProps = map[int]bool{19: true, 20: true}

type ExtractedData struct {
	ID    int
	Times []TimeRecord
	Links []LinkRecord
	Place []PlaceRecord
	Query []QueryRecord
}

type TimeRecord struct {
	Code                 int
	Y, M, D, H, Min, Sec int
}

type LinkRecord struct {
	Code, Value int
}

type PlaceRecord struct {
	Code      int
	Lat, Lon  float64
	Precision float64
}

type QueryRecord struct {
	Lang, Label, Data string
}

type SearchParams struct {
	Lang             string
	QueryText        string
	Day, Month, Year int
	Lat, Lon         float64
	Range            int // 0=Specific, 1=Before, 2=After
	Limit            int
}

type SearchResult struct {
	ID    int     `json:"id"`
	Code  int     `json:"code"`
	Lat   float64 `json:"latitude"`
	Lon   float64 `json:"longitude"`
	Day   int     `json:"day"`
	Month int     `json:"month"`
	Year  int     `json:"year"`
	Label string  `json:"label"`
	Data  string  `json:"data"`
}

type Entity struct {
	ID           string             `json:"id"`
	Claims       map[string][]Claim `json:"claims"`
	Labels       map[string]Term    `json:"labels"`
	Descriptions map[string]Term    `json:"descriptions"`
}

type Claim struct {
	Mainsnak Snak `json:"mainsnak"`
}

type Snak struct {
	Datavalue Datavalue `json:"datavalue"`
}

type Datavalue struct {
	Value json.RawMessage `json:"value"`
}

type TimeValue struct {
	Time string `json:"time"`
}

type CoordinateValue struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Precision float64 `json:"precision"`
}

type EntityIdValue struct {
	ID string `json:"id"`
}

type Term struct {
	Value string `json:"value"`
}

type MapConfig struct {
	CenterLng float64
	CenterLat float64
	Zoom      float64
	MinZoom   int
	MaxZoom   int
}
