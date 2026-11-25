// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package web

import (
	"embed"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:assets
var assetsFS embed.FS

func ServeAssets(w http.ResponseWriter, r *http.Request) {
	urlPath := path.Clean(r.URL.Path)
	if urlPath == "/" || urlPath == "." {
		urlPath = "/index.html"
	}

	targetPath := path.Join("assets", strings.TrimPrefix(urlPath, "/"))

	var data []byte
	var serveGzip bool

	f, err := assetsFS.Open(targetPath)
	if err == nil {
		fi, statErr := f.Stat()
		if statErr != nil || fi.IsDir() {
			f.Close()
			http.NotFound(w, r)
			return
		}
		data, err = io.ReadAll(f)
		f.Close()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(targetPath, ".gz") {
			serveGzip = true
		}
	} else {
		gzPath := targetPath + ".gz"
		gzFile, gzErr := assetsFS.Open(gzPath)
		if gzErr != nil {
			http.NotFound(w, r)
			return
		}
		data, err = io.ReadAll(gzFile)
		gzFile.Close()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		serveGzip = true
	}

	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		serveGzip = true
	}

	origName := strings.TrimSuffix(urlPath, ".gz")
	ext := strings.ToLower(path.Ext(origName))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		switch ext {
		case ".css":
			contentType = "text/css; charset=utf-8"
		case ".js":
			contentType = "application/javascript; charset=utf-8"
		case ".json":
			contentType = "application/json"
		case ".svg":
			contentType = "image/svg+xml"
		case ".pbf":
			contentType = "application/x-protobuf"
		case ".html":
			contentType = "text/html; charset=utf-8"
		default:
			contentType = "application/octet-stream"
		}
	}

	w.Header().Set("Content-Type", contentType)
	if serveGzip {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
