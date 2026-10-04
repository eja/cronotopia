// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

type Config struct {
	dbPath              string
	language            string
	limit               int
	log                 bool
	logFile             string
	webHost             string
	webPort             int
	wikipediaImport     string
	wikidataImport      string
	wikiliteImport      string
	mbtilesImport       string
	ggufImport          string
	aiAnn               bool
	aiAnnSize           int
	aiApi               bool
	aiApiKey            string
	aiApiUrl            string
	aiCache             bool
	aiModel             string
	aiModelPrefixSave   string
	aiModelPrefixSearch string
	aiSync              bool
}
