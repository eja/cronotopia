package main

import (
	"fmt"
	"io"
	"strconv"
	"time"
)

type countingReader struct {
	r     io.Reader
	count *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		*c.count += int64(n)
	}
	return n, err
}

type wrappedReadCloser struct {
	io.Reader
	io.Closer
}

func (w *wrappedReadCloser) Read(p []byte) (n int, err error) {
	return w.Reader.Read(p)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func parseID(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	val, err := strconv.Atoi(s[1:])
	return val, err == nil
}
