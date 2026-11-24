// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package vfs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/SaveTheRbtz/zstd-seekable-format-go/pkg/framecache"
	"github.com/klauspost/compress/zstd"
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

func IsSQLiteHeader(header []byte) bool {
	return len(header) >= 16 && string(header[:16]) == "SQLite format 3\x00"
}

type AttachedTarget struct {
	Schema         string
	Path           string
	URI            string
	IsZst          bool
	ZstFile        *os.File
	ZstdDec        *zstd.Decoder
	SeekableReader io.Closer
}

func (t *AttachedTarget) Close() error {
	var firstErr error
	if t.SeekableReader != nil {
		if err := t.SeekableReader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if t.ZstdDec != nil {
		t.ZstdDec.Close()
	}
	if t.ZstFile != nil {
		if err := t.ZstFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func PrepareTarget(idx int, filePath string) (*AttachedTarget, error) {
	if filePath == "" {
		return nil, nil
	}

	absPath, err := filepath.Abs(filePath)
	if err == nil {
		filePath = absPath
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database (%s): %w", filePath, err)
	}

	header := make([]byte, 16)
	n, _ := f.ReadAt(header, 0)
	header = header[:n]

	target := &AttachedTarget{
		Path: filePath,
	}

	if IsZstdHeader(header) {
		target.IsZst = true
		target.ZstFile = f
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to initialize zstd decoder (%s): %w", filePath, err)
		}
		target.ZstdDec = dec

		fCache := framecache.NewFIFO(framecache.Limits{MaxFrames: 128})
		sReader, err := seekable.NewReader(f, dec, seekable.WithReaderFrameCache(fCache))
		if err != nil {
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to create seekable reader (%s): %w", filePath, err)
		}
		target.SeekableReader = sReader

		uncompressedSize, err := sReader.Seek(0, io.SeekEnd)
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to seek end (%s): %w", filePath, err)
		}
		if _, err := sReader.Seek(0, io.SeekStart); err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to seek start (%s): %w", filePath, err)
		}

		vfs, err := GetOrInitVFS()
		if err != nil {
			sReader.Close()
			dec.Close()
			f.Close()
			return nil, fmt.Errorf("failed to initialize global VFS: %w", err)
		}

		virtualName := fmt.Sprintf("attached_%d.sqlite", idx)
		GlobalRouter.Register(virtualName, NewLockedReaderAt(sReader), uncompressedSize)
		target.URI = fmt.Sprintf("file:%s?vfs=%s&mode=ro&immutable=1", virtualName, vfs)
	} else if IsSQLiteHeader(header) {
		f.Close()
		cleanPath := filepath.ToSlash(filePath)
		if !strings.HasPrefix(cleanPath, "file:") {
			target.URI = fmt.Sprintf("file:%s?mode=ro&immutable=1", cleanPath)
		} else if !strings.Contains(cleanPath, "immutable=") {
			if strings.Contains(cleanPath, "?") {
				target.URI = cleanPath + "&mode=ro&immutable=1"
			} else {
				target.URI = cleanPath + "?mode=ro&immutable=1"
			}
		} else {
			target.URI = cleanPath
		}
	} else {
		f.Close()
		return nil, fmt.Errorf("unsupported database format (%s)", filePath)
	}

	return target, nil
}
