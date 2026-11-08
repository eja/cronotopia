// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"zombiezen.com/go/sqlite/sqlitex"
)

type etopoGrid struct {
	width  int
	height int
	data   []int16
}

type tiffTag struct {
	tag      uint16
	dataType uint16
	count    uint64
	offset   uint64
}

func readTiffValue(r io.ReaderAt, bo binary.ByteOrder, offset uint64, dataType uint16) (uint64, error) {
	switch dataType {
	case 1:
		b := make([]byte, 1)
		if _, err := r.ReadAt(b, int64(offset)); err != nil {
			return 0, err
		}
		return uint64(b[0]), nil
	case 3:
		b := make([]byte, 2)
		if _, err := r.ReadAt(b, int64(offset)); err != nil {
			return 0, err
		}
		return uint64(bo.Uint16(b)), nil
	case 4:
		b := make([]byte, 4)
		if _, err := r.ReadAt(b, int64(offset)); err != nil {
			return 0, err
		}
		return uint64(bo.Uint32(b)), nil
	case 16:
		b := make([]byte, 8)
		if _, err := r.ReadAt(b, int64(offset)); err != nil {
			return 0, err
		}
		return bo.Uint64(b), nil
	default:
		b := make([]byte, 4)
		if _, err := r.ReadAt(b, int64(offset)); err != nil {
			return 0, err
		}
		return uint64(bo.Uint32(b)), nil
	}
}

func readTiffSlice(r io.ReaderAt, bo binary.ByteOrder, offset uint64, count uint64, dataType uint16) ([]uint64, error) {
	out := make([]uint64, count)
	for i := uint64(0); i < count; i++ {
		step := uint64(4)
		if dataType == 1 {
			step = 1
		} else if dataType == 3 {
			step = 2
		} else if dataType == 16 {
			step = 8
		}
		val, err := readTiffValue(r, bo, offset+i*step, dataType)
		if err != nil {
			return nil, err
		}
		out[i] = val
	}
	return out, nil
}

func decodeRowSamples(row []byte, width int, bytesPerSample int, predictor int, bo binary.ByteOrder, out []int16) {
	rowBytes := width * bytesPerSample

	if predictor == 2 && bytesPerSample == 2 {
		for c := 2; c < rowBytes; c += 2 {
			val := bo.Uint16(row[c:])
			prev := bo.Uint16(row[c-2:])
			bo.PutUint16(row[c:], val+prev)
		}
		for c := 0; c < width; c++ {
			out[c] = int16(bo.Uint16(row[c*2:]))
		}
		return
	}

	if predictor == 3 && bytesPerSample == 4 {
		for i := 1; i < rowBytes; i++ {
			row[i] += row[i-1]
		}
		for c := 0; c < width; c++ {
			b0 := uint32(row[c])
			b1 := uint32(row[width+c])
			b2 := uint32(row[2*width+c])
			b3 := uint32(row[3*width+c])
			bits := (b0 << 24) | (b1 << 16) | (b2 << 8) | b3
			fVal := math.Float32frombits(bits)
			if math.IsNaN(float64(fVal)) || math.IsInf(float64(fVal), 0) || fVal < -12000 || fVal > 9000 {
				fVal = 0
			}
			out[c] = int16(math.Round(float64(fVal)))
		}
		return
	}

	if bytesPerSample == 2 {
		for c := 0; c < width; c++ {
			out[c] = int16(bo.Uint16(row[c*2:]))
		}
	} else if bytesPerSample == 4 {
		for c := 0; c < width; c++ {
			bits := bo.Uint32(row[c*4:])
			fVal := math.Float32frombits(bits)
			if math.IsNaN(float64(fVal)) || math.IsInf(float64(fVal), 0) || fVal < -12000 || fVal > 9000 {
				fVal = 0
			}
			out[c] = int16(math.Round(float64(fVal)))
		}
	}
}

func loadETOPO(filePath string) (*etopoGrid, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	header := make([]byte, 8)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, err
	}

	var bo binary.ByteOrder
	if header[0] == 'I' && header[1] == 'I' {
		bo = binary.LittleEndian
	} else if header[0] == 'M' && header[1] == 'M' {
		bo = binary.BigEndian
	} else {
		return nil, fmt.Errorf("invalid tiff header")
	}

	magic := bo.Uint16(header[2:4])
	isBigTIFF := false
	var ifdOffset uint64

	if magic == 42 {
		ifdOffset = uint64(bo.Uint32(header[4:8]))
	} else if magic == 43 {
		isBigTIFF = true
		bigHeader := make([]byte, 8)
		if _, err := io.ReadFull(f, bigHeader); err != nil {
			return nil, err
		}
		ifdOffset = bo.Uint64(bigHeader[0:8])
	} else {
		return nil, fmt.Errorf("unsupported tiff version: %d", magic)
	}

	tags := make(map[uint16]tiffTag)

	if !isBigTIFF {
		countBytes := make([]byte, 2)
		if _, err := f.ReadAt(countBytes, int64(ifdOffset)); err != nil {
			return nil, err
		}
		entryCount := bo.Uint16(countBytes)
		currOffset := ifdOffset + 2

		for i := uint16(0); i < entryCount; i++ {
			entryBuf := make([]byte, 12)
			if _, err := f.ReadAt(entryBuf, int64(currOffset)); err != nil {
				return nil, err
			}
			tagID := bo.Uint16(entryBuf[0:2])
			dataType := bo.Uint16(entryBuf[2:4])
			count := uint64(bo.Uint32(entryBuf[4:8]))

			var valOffset uint64
			itemSize := uint64(4)
			if dataType == 1 {
				itemSize = 1
			} else if dataType == 3 {
				itemSize = 2
			}
			totalSize := count * itemSize

			if totalSize <= 4 {
				valOffset = currOffset + 8
			} else {
				valOffset = uint64(bo.Uint32(entryBuf[8:12]))
			}

			tags[tagID] = tiffTag{
				tag:      tagID,
				dataType: dataType,
				count:    count,
				offset:   valOffset,
			}
			currOffset += 12
		}
	} else {
		countBytes := make([]byte, 8)
		if _, err := f.ReadAt(countBytes, int64(ifdOffset)); err != nil {
			return nil, err
		}
		entryCount := bo.Uint64(countBytes)
		currOffset := ifdOffset + 8

		for i := uint64(0); i < entryCount; i++ {
			entryBuf := make([]byte, 20)
			if _, err := f.ReadAt(entryBuf, int64(currOffset)); err != nil {
				return nil, err
			}
			tagID := bo.Uint16(entryBuf[0:2])
			dataType := bo.Uint16(entryBuf[2:4])
			count := bo.Uint64(entryBuf[4:12])

			var valOffset uint64
			itemSize := uint64(4)
			if dataType == 1 {
				itemSize = 1
			} else if dataType == 3 {
				itemSize = 2
			} else if dataType == 16 {
				itemSize = 8
			}
			totalSize := count * itemSize

			if totalSize <= 8 {
				valOffset = currOffset + 12
			} else {
				valOffset = bo.Uint64(entryBuf[12:20])
			}

			tags[tagID] = tiffTag{
				tag:      tagID,
				dataType: dataType,
				count:    count,
				offset:   valOffset,
			}
			currOffset += 20
		}
	}

	widthVal, _ := readTiffValue(f, bo, tags[256].offset, tags[256].dataType)
	heightVal, _ := readTiffValue(f, bo, tags[257].offset, tags[257].dataType)
	bitsVal, _ := readTiffValue(f, bo, tags[258].offset, tags[258].dataType)
	compVal, _ := readTiffValue(f, bo, tags[259].offset, tags[259].dataType)

	width := int(widthVal)
	height := int(heightVal)
	bits := int(bitsVal)
	compression := int(compVal)

	if width == 0 || height == 0 {
		return nil, fmt.Errorf("invalid tiff dimensions: %dx%d", width, height)
	}

	predictorVal, _ := readTiffValue(f, bo, tags[317].offset, tags[317].dataType)
	predictor := int(predictorVal)

	grid := &etopoGrid{
		width:  width,
		height: height,
		data:   make([]int16, width*height),
	}

	isTiled := tags[322].count > 0 && tags[323].count > 0

	if isTiled {
		tileWVal, _ := readTiffValue(f, bo, tags[322].offset, tags[322].dataType)
		tileHVal, _ := readTiffValue(f, bo, tags[323].offset, tags[323].dataType)
		tileW := int(tileWVal)
		tileH := int(tileHVal)

		tileOffsets, err := readTiffSlice(f, bo, tags[324].offset, tags[324].count, tags[324].dataType)
		if err != nil {
			return nil, err
		}
		tileByteCounts, err := readTiffSlice(f, bo, tags[325].offset, tags[325].count, tags[325].dataType)
		if err != nil {
			return nil, err
		}

		tilesX := (width + tileW - 1) / tileW
		tilesY := (height + tileH - 1) / tileH

		bytesPerSample := bits / 8
		tileUncompressedSize := tileW * tileH * bytesPerSample
		rowLineBuf := make([]int16, tileW)

		for ty := 0; ty < tilesY; ty++ {
			for tx := 0; tx < tilesX; tx++ {
				idx := ty*tilesX + tx
				rawBuf := make([]byte, tileByteCounts[idx])
				if _, err := f.ReadAt(rawBuf, int64(tileOffsets[idx])); err != nil {
					return nil, err
				}

				var decompressed []byte
				if compression == 1 {
					decompressed = rawBuf
				} else if compression == 8 || compression == 32946 {
					zr, zErr := zlib.NewReader(bytes.NewReader(rawBuf))
					if zErr != nil {
						return nil, zErr
					}
					decompressed = make([]byte, tileUncompressedSize)
					_, _ = io.ReadFull(zr, decompressed)
					zr.Close()
				} else {
					return nil, fmt.Errorf("unsupported compression: %d", compression)
				}

				tileRowBytes := tileW * bytesPerSample

				for r := 0; r < tileH; r++ {
					targetY := ty*tileH + r
					if targetY >= height {
						break
					}
					rowSlice := decompressed[r*tileRowBytes : (r+1)*tileRowBytes]
					decodeRowSamples(rowSlice, tileW, bytesPerSample, predictor, bo, rowLineBuf)

					targetRowOff := targetY * width
					for c := 0; c < tileW; c++ {
						targetX := tx*tileW + c
						if targetX >= width {
							break
						}
						grid.data[targetRowOff+targetX] = rowLineBuf[c]
					}
				}
			}
		}
	} else {
		stripOffsets, err := readTiffSlice(f, bo, tags[273].offset, tags[273].count, tags[273].dataType)
		if err != nil {
			return nil, err
		}
		stripByteCounts, err := readTiffSlice(f, bo, tags[279].offset, tags[279].count, tags[279].dataType)
		if err != nil {
			return nil, err
		}
		rowsPerStripVal, _ := readTiffValue(f, bo, tags[278].offset, tags[278].dataType)
		rowsPerStrip := int(rowsPerStripVal)
		if rowsPerStrip == 0 {
			rowsPerStrip = height
		}

		bytesPerSample := bits / 8
		currentY := 0
		rowLineBuf := make([]int16, width)

		for sIdx := 0; sIdx < len(stripOffsets); sIdx++ {
			rawBuf := make([]byte, stripByteCounts[sIdx])
			if _, err := f.ReadAt(rawBuf, int64(stripOffsets[sIdx])); err != nil {
				return nil, err
			}

			stripRows := rowsPerStrip
			if currentY+stripRows > height {
				stripRows = height - currentY
			}
			expectedSize := stripRows * width * bytesPerSample

			var decompressed []byte
			if compression == 1 {
				decompressed = rawBuf
			} else if compression == 8 || compression == 32946 {
				zr, zErr := zlib.NewReader(bytes.NewReader(rawBuf))
				if zErr != nil {
					return nil, zErr
				}
				decompressed = make([]byte, expectedSize)
				_, _ = io.ReadFull(zr, decompressed)
				zr.Close()
			} else {
				return nil, fmt.Errorf("unsupported compression: %d", compression)
			}

			stripRowBytes := width * bytesPerSample

			for r := 0; r < stripRows; r++ {
				targetY := currentY + r
				if targetY >= height {
					break
				}
				rowSlice := decompressed[r*stripRowBytes : (r+1)*stripRowBytes]
				decodeRowSamples(rowSlice, width, bytesPerSample, predictor, bo, rowLineBuf)

				targetRowOff := targetY * width
				copy(grid.data[targetRowOff:targetRowOff+width], rowLineBuf)
			}
			currentY += stripRows
		}
	}

	return grid, nil
}

type sampleCoord struct {
	i0, i1 int
	frac   float32
}

func RunETOPOImport(db *DBHandler, filePath string) error {
	log.Printf("Reading ETOPO GeoTIFF: %s", filePath)
	start := time.Now()

	grid, err := loadETOPO(filePath)
	if err != nil {
		return fmt.Errorf("failed to load etopo geotiff: %w", err)
	}

	log.Printf("Loaded %dx%d raster in %s. Generating smooth interpolated tiles (Zoom 0-7)...",
		grid.width, grid.height, time.Since(start).Round(time.Millisecond))

	conn := db.pool.Get(context.Background())
	if conn == nil {
		return fmt.Errorf("failed to get database connection")
	}
	defer db.pool.Put(conn)

	_ = sqlitex.Execute(conn, "DELETE FROM elevations", nil)

	const maxZ = 7
	workers := runtime.NumCPU()

	for z := 0; z <= maxZ; z++ {
		worldSize := 256 * (1 << z)
		cols := make([]sampleCoord, worldSize)
		rows := make([]sampleCoord, worldSize)

		for gx := 0; gx < worldSize; gx++ {
			fx := (float64(gx) / float64(worldSize)) * float64(grid.width)
			i0 := int(math.Floor(fx))
			frac := float32(fx - float64(i0))
			i1 := (i0 + 1) % grid.width
			if i0 < 0 {
				i0 = 0
			} else if i0 >= grid.width {
				i0 = grid.width - 1
			}
			cols[gx] = sampleCoord{i0: i0, i1: i1, frac: frac}
		}

		for gy := 0; gy < worldSize; gy++ {
			v := float64(gy) / float64(worldSize)
			latRad := math.Atan(math.Sinh(math.Pi * (1.0 - 2.0*v)))
			lat := latRad * 180.0 / math.Pi
			fy := ((90.0 - lat) / 180.0) * float64(grid.height)
			if fy < 0 {
				fy = 0
			} else if fy > float64(grid.height-1) {
				fy = float64(grid.height - 1)
			}
			i0 := int(math.Floor(fy))
			frac := float32(fy - float64(i0))
			i1 := i0 + 1
			if i1 >= grid.height {
				i1 = grid.height - 1
			}
			rows[gy] = sampleCoord{i0: i0, i1: i1, frac: frac}
		}

		nTiles := 1 << z
		type tileTask struct {
			x, y int
		}
		type tileOutput struct {
			x, y int
			blob []byte
		}

		taskCh := make(chan tileTask, nTiles*nTiles)
		outCh := make(chan tileOutput, 512)
		var wg sync.WaitGroup

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				img := image.NewRGBA(image.Rect(0, 0, 256, 256))
				var buf bytes.Buffer

				for task := range taskCh {
					buf.Reset()
					for py := 0; py < 256; py++ {
						gy := task.y*256 + py
						r := rows[gy]
						row0 := r.i0 * grid.width
						row1 := r.i1 * grid.width
						ry0 := 1.0 - r.frac
						ry1 := r.frac

						for px := 0; px < 256; px++ {
							gx := task.x*256 + px
							c := cols[gx]

							v00 := float32(grid.data[row0+c.i0])
							v10 := float32(grid.data[row0+c.i1])
							v01 := float32(grid.data[row1+c.i0])
							v11 := float32(grid.data[row1+c.i1])

							top := v00*(1.0-c.frac) + v10*c.frac
							bot := v01*(1.0-c.frac) + v11*c.frac
							elev := top*ry0 + bot*ry1

							val := float64(elev) + 32768.0
							if val < 0 {
								val = 0
							} else if val > 65535.99 {
								val = 65535.99
							}

							ival := int(math.Floor(val))
							fracByte := uint8(math.Floor((val - float64(ival)) * 256.0))

							img.SetRGBA(px, py, color.RGBA{
								R: uint8(ival >> 8),
								G: uint8(ival & 0xFF),
								B: fracByte,
								A: 255,
							})
						}
					}

					_ = png.Encode(&buf, img)
					tileBytes := make([]byte, buf.Len())
					copy(tileBytes, buf.Bytes())
					outCh <- tileOutput{x: task.x, y: task.y, blob: tileBytes}
				}
			}()
		}

		go func() {
			for tx := 0; tx < nTiles; tx++ {
				for ty := 0; ty < nTiles; ty++ {
					taskCh <- tileTask{x: tx, y: ty}
				}
			}
			close(taskCh)
			wg.Wait()
			close(outCh)
		}()

		endTx, txErr := sqlitex.ImmediateTransaction(conn)
		if txErr != nil {
			return txErr
		}

		for out := range outCh {
			yTms := (1 << z) - 1 - out.y
			_ = sqlitex.Execute(conn, "INSERT INTO elevations (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)", &sqlitex.ExecOptions{
				Args: []any{z, out.x, yTms, out.blob},
			})
		}
		endTx(&txErr)
		if txErr != nil {
			return txErr
		}
		log.Printf("Zoom level %d complete (%d tiles)", z, nTiles*nTiles)
	}

	log.Printf("ETOPO import complete in %s", time.Since(start).Round(time.Second))
	return nil
}
