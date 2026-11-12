// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"runtime"
)

type Config struct {
	dbPath              string
	compress            bool
	decompress          bool
	log                 bool
	logFile             string
	webHost             string
	webPort             int
	wikipediaImport     string
	wikidataImport      string
	wikiliteImport      string
	pmtilesImport       string
	etopoImport         string
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
	threads             int
	zoom                int
}

func numWorkers() int {
	if options != nil && options.threads > 0 {
		return options.threads
	}
	return runtime.GOMAXPROCS(0)
}
