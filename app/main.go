// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

const Name = "Cronotopia"
const Version = "8.1.22"

func main() {
	var importSrc, dbFile, langStr, logFile, webHost, webPort, webTlsPublic, webTlsPrivate string
	var logging bool

	flag.StringVar(&importSrc, "import", "", "Wikidata dump url or file")
	flag.StringVar(&dbFile, "db", "cronotopia.db", "DB path")
	flag.StringVar(&langStr, "language", "en", "Comma separated list of importing languages")
	flag.BoolVar(&logging, "log", false, "Enable logging")
	flag.StringVar(&logFile, "log-file", "", "Log to file")
	flag.StringVar(&webHost, "web-host", "localhost", "Web server host.")
	flag.StringVar(&webPort, "web-port", "35248", "Web server port.")
	flag.StringVar(&webTlsPublic, "web-tls-public", "", "Web TLS public certificate")
	flag.StringVar(&webTlsPrivate, "web-tls-private", "", "Web TLS private certificate")

	flag.Usage = func() {
		fmt.Println("Copyright:", "2018-2026 by Ubaldo Porcheddu <ubaldo@eja.it>")
		fmt.Println("Version:", Version)
		fmt.Printf("Usage: %s [options]\n", os.Args[0])
		fmt.Println("Options:\n")
		flag.PrintDefaults()
		fmt.Println()
	}
	flag.Parse()

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		log.SetOutput(f)
		logging = true
	}

	if importSrc != "" {
		runImport(importSrc, dbFile, langStr, logging)
	} else {
		runServer(dbFile, webHost, webPort, webTlsPrivate, webTlsPublic)
	}
}
