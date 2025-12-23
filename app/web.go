package main

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed assets
var assetsFS embed.FS

type hybridListener struct {
	net.Listener
	config *tls.Config
}

func (l *hybridListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		br := bufio.NewReader(conn)
		peek, err := br.Peek(1)
		if err != nil {
			conn.Close()
			continue
		}

		if peek[0] == 0x16 {
			return tls.Server(combinedConn{br, conn}, l.config), nil
		}

		return combinedConn{br, conn}, nil
	}
}

type combinedConn struct {
	io.Reader
	net.Conn
}

func (c combinedConn) Read(b []byte) (int, error) { return c.Reader.Read(b) }

func generateSelfSigned() (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	notBefore := time.Unix(0, 0)
	notAfter := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Auto-Signed Server"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return tls.X509KeyPair(certPEM, keyPEM)
}

type GzipFileServer struct {
	fs http.FileSystem
}

func (gfs *GzipFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	gzPath := path + ".gz"
	gzFile, err := gfs.fs.Open(gzPath)
	if err == nil {
		defer gzFile.Close()

		if stat, err := gzFile.Stat(); err == nil && !stat.IsDir() {
			if acceptsGzip(r) {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", getContentType(path))
				w.Header().Set("Vary", "Accept-Encoding")
				http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), gzFile)
				return
			}

			gzReader, err := gzip.NewReader(gzFile)
			if err != nil {
				http.Error(w, "Failed to decompress gzip file", http.StatusInternalServerError)
				return
			}
			defer gzReader.Close()

			w.Header().Set("Content-Type", getContentType(path))
			io.Copy(w, gzReader)
			return
		}
	}

	file, err := gfs.fs.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", getContentType(path))
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), file)
}

func acceptsGzip(r *http.Request) bool {
	acceptEncoding := r.Header.Get("Accept-Encoding")
	return strings.Contains(acceptEncoding, "gzip")
}

func getContentType(path string) string {
	ctype := mime.TypeByExtension(filepath.Ext(path))
	if ctype == "" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".html", ".htm":
			return "text/html; charset=utf-8"
		case ".css":
			return "text/css; charset=utf-8"
		case ".js":
			return "application/javascript"
		case ".json":
			return "application/json"
		case ".svg":
			return "image/svg+xml"
		case ".woff":
			return "font/woff"
		case ".woff2":
			return "font/woff2"
		case ".txt":
			return "text/plain; charset=utf-8"
		case ".xml":
			return "application/xml"
		case ".pbf":
			return "application/x-protobuf"
		default:
			return "application/octet-stream"
		}
	}
	return ctype
}

func handleAPI(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	query := r.URL.Query()

	params := SearchParams{
		Lang:      query.Get("language"),
		QueryText: query.Get("query"),
	}

	if params.Lang == "" {
		params.Lang = "en"
	}

	params.Day, _ = strconv.Atoi(query.Get("day"))
	params.Month, _ = strconv.Atoi(query.Get("month"))
	params.Year, _ = strconv.Atoi(query.Get("year"))
	params.Lat, _ = strconv.ParseFloat(query.Get("latitude"), 64)
	params.Lon, _ = strconv.ParseFloat(query.Get("longitude"), 64)
	params.Range, _ = strconv.Atoi(query.Get("range"))
	params.Limit, _ = strconv.Atoi(query.Get("limit"))

	if params.Limit <= 0 {
		params.Limit = 100
	}

	results, err := SearchEvents(db, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if results == nil {
		w.Write([]byte("[]"))
	} else {
		json.NewEncoder(w).Encode(results)
	}
}

func serveTiles(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	z, _ := strconv.Atoi(parts[2])
	x, _ := strconv.Atoi(parts[3])
	yStr := strings.TrimSuffix(parts[4], ".pbf")
	y, _ := strconv.Atoi(yStr)

	tileData, err := GetTileData(db, z, x, y)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if len(tileData) > 2 {
		if tileData[0] == 0x1f && tileData[1] == 0x8b {
			w.Header().Set("Content-Encoding", "gzip")
		} else if tileData[0] == 0x78 {
			w.Header().Set("Content-Encoding", "deflate")
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(tileData)
}

func runServer(dbFile, host, port, privateTLS, publicTLS string) {
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		log.Fatalf("Database file %s not found.", dbFile)
	}

	log.Printf("Connecting to database %s...", dbFile)
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(20)
	defer db.Close()

	mapConfig := GetMapConfig(db)
	log.Printf("Map Config: Center [%.4f, %.4f], Zoom %.1f, Min: %d, Max: %d", mapConfig.CenterLng, mapConfig.CenterLat, mapConfig.Zoom, mapConfig.MinZoom, mapConfig.MaxZoom)

	assetsRoot, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatal("Failed to load embedded assets:", err)
	}

	tmpl, err := template.ParseFS(assetsFS, "assets/index.html")
	if err != nil {
		log.Fatal("Failed to parse template:", err)
	}

	gzipFileServer := &GzipFileServer{fs: http.FS(assetsRoot)}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			gzipFileServer.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		tmpl.Execute(w, mapConfig)
	})

	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		handleAPI(w, r, db)
	})

	mux.HandleFunc("/tiles/", func(w http.ResponseWriter, r *http.Request) {
		serveTiles(w, r, db)
	})

	var cert tls.Certificate
	if privateTLS != "" && publicTLS != "" {
		cert, err = tls.LoadX509KeyPair(publicTLS, privateTLS)
		if err != nil {
			log.Fatalf("Failed to load TLS cert: %v", err)
		}
		log.Printf("Using provided TLS certificats")
	} else {
		cert, err = generateSelfSigned()
		if err != nil {
			log.Fatalf("Failed to generate self-signed cert: %v", err)
		}
		log.Printf("Generated self-signed certificate")
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	addr := fmt.Sprintf("%s:%s", host, port)
	baseListener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}

	hListener := &hybridListener{
		Listener: baseListener,
		config:   tlsConfig,
	}

	server := &http.Server{Handler: mux}
	log.Printf("Server listening on %s", addr)
	log.Fatal(server.Serve(hListener))
}
