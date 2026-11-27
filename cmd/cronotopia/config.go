// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"runtime"
	"strings"
)

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(val string) error {
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

type Config struct {
	dbPath              stringSlice
	dbShell             bool
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
	mediaImport         string
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
