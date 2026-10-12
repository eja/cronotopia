// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
)

const (
	seekableFrameSize = 128 * 1024
)

type decompressCountReader struct {
	r io.Reader
	n int64
}

func (c *decompressCountReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func CompressDB(srcPath, dstPath string) error {
	if srcPath == dstPath {
		return fmt.Errorf("source and destination paths must be different")
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer src.Close()

	srcStat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination: %w", err)
	}
	defer dst.Close()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return fmt.Errorf("failed to create zstd encoder: %w", err)
	}
	defer enc.Close()

	w, err := seekable.NewWriter(dst, enc)
	if err != nil {
		return fmt.Errorf("failed to create seekable writer: %w", err)
	}

	start := time.Now()
	log.Printf("Compressing %s (%d bytes) -> %s...", srcPath, srcStat.Size(), dstPath)

	buf := make([]byte, seekableFrameSize)
	var written int64
	nextMilestone := 10

	for {
		n, rErr := io.ReadFull(src, buf)
		if n > 0 {
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				return fmt.Errorf("write error: %w", wErr)
			}
			written += int64(n)

			if srcStat.Size() > 0 {
				pct := int(written * 100 / srcStat.Size())
				if pct >= nextMilestone && nextMilestone <= 100 {
					log.Printf("Compressing progress: %d%% (%d / %d MB)",
						nextMilestone, written/(1024*1024), srcStat.Size()/(1024*1024))
					nextMilestone = (pct/10)*10 + 10
				}
			}
		}
		if rErr == io.EOF || rErr == io.ErrUnexpectedEOF {
			break
		}
		if rErr != nil {
			return fmt.Errorf("read error: %w", rErr)
		}
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to finalize seekable table: %w", err)
	}

	dstStat, err := dst.Stat()
	ratio := 0.0
	if srcStat.Size() > 0 && err == nil {
		ratio = float64(dstStat.Size()) / float64(srcStat.Size()) * 100.0
	}

	elapsed := time.Since(start)
	log.Printf("Compress completed in %s: %d -> %d bytes (%.1f%%)",
		elapsed.Round(time.Millisecond), srcStat.Size(), dstStat.Size(), ratio)
	return nil
}

func DecompressDB(srcPath, dstPath string) error {
	if srcPath == dstPath {
		return fmt.Errorf("source and destination paths must be different")
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer src.Close()

	srcStat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination: %w", err)
	}
	defer dst.Close()

	cr := &decompressCountReader{r: src}
	dec, err := zstd.NewReader(cr)
	if err != nil {
		return fmt.Errorf("failed to create zstd reader: %w", err)
	}
	defer dec.Close()

	start := time.Now()
	log.Printf("Decompressing %s (%d bytes) -> %s...", srcPath, srcStat.Size(), dstPath)

	buf := make([]byte, 1024*1024)
	var written int64
	nextMilestone := 10

	for {
		nr, rErr := dec.Read(buf)
		if nr > 0 {
			nw, wErr := dst.Write(buf[:nr])
			if wErr != nil {
				return fmt.Errorf("write error: %w", wErr)
			}
			written += int64(nw)

			if srcStat.Size() > 0 {
				pct := int(cr.n * 100 / srcStat.Size())
				if pct >= nextMilestone && nextMilestone <= 100 {
					log.Printf("Decompressing progress: %d%% (%d MB written)", nextMilestone, written/(1024*1024))
					nextMilestone = (pct/10)*10 + 10
				}
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return fmt.Errorf("decompress error: %w", rErr)
		}
	}

	elapsed := time.Since(start)
	log.Printf("Decompress completed in %s: %d bytes written", elapsed.Round(time.Millisecond), written)
	return nil
}
