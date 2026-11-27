// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package vfs

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	modernc "modernc.org/sqlite/vfs"
)

type LockedReaderAt struct {
	mu sync.Mutex
	r  io.ReaderAt
}

func (l *LockedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.ReadAt(p, off)
}

func NewLockedReaderAt(r io.ReaderAt) *LockedReaderAt {
	return &LockedReaderAt{r: r}
}

type vfsFileEntry struct {
	r    io.ReaderAt
	size int64
}

type router struct {
	mu    sync.RWMutex
	files map[string]*vfsFileEntry
}

var (
	GlobalRouter = &router{files: make(map[string]*vfsFileEntry)}
	vfsName      string
	vfsHandle    io.Closer
	vfsOnce      sync.Once
	vfsErr       error
)

func GetOrInitVFS() (string, error) {
	vfsOnce.Do(func() {
		name, handle, err := modernc.New(GlobalRouter)
		if err != nil {
			vfsErr = err
			return
		}
		vfsName = name
		vfsHandle = handle
	})
	return vfsName, vfsErr
}

func (vr *router) Register(name string, r io.ReaderAt, size int64) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	vr.files[name] = &vfsFileEntry{r: r, size: size}
}

func (vr *router) Open(name string) (fs.File, error) {
	clean := strings.TrimPrefix(filepath.ToSlash(name), "/")
	base := filepath.Base(clean)

	vr.mu.RLock()
	entry, ok := vr.files[clean]
	if !ok {
		entry, ok = vr.files[base]
	}
	vr.mu.RUnlock()

	if ok {
		return &readerFile{
			SectionReader: io.NewSectionReader(entry.r, 0, entry.size),
			size:          entry.size,
		}, nil
	}

	f, err := os.Open(name)
	if err == nil {
		return f, nil
	}
	return nil, fs.ErrNotExist
}

type readerFile struct {
	*io.SectionReader
	size int64
}

func (f *readerFile) Stat() (fs.FileInfo, error) {
	return &readerFileInfo{size: f.size}, nil
}

func (f *readerFile) Close() error {
	return nil
}

type readerFileInfo struct {
	size int64
}

func (fi *readerFileInfo) Name() string       { return "db" }
func (fi *readerFileInfo) Size() int64        { return fi.size }
func (fi *readerFileInfo) Mode() fs.FileMode  { return 0444 }
func (fi *readerFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *readerFileInfo) IsDir() bool        { return false }
func (fi *readerFileInfo) Sys() any           { return nil }

func IsZstdHeader(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	if header[0] == 0x28 && header[1] == 0xb5 && header[2] == 0x2f && header[3] == 0xfd {
		return true
	}
	if header[1] == 0x2a && header[2] == 0x4d && header[3] == 0x18 && (header[0]&0xf0) == 0x50 {
		return true
	}
	return false
}
