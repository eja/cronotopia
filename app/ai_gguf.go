// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	GGUFTypeUint8   uint32 = 0
	GGUFTypeInt8    uint32 = 1
	GGUFTypeUint16  uint32 = 2
	GGUFTypeInt16   uint32 = 3
	GGUFTypeUint32  uint32 = 4
	GGUFTypeInt32   uint32 = 5
	GGUFTypeFloat32 uint32 = 6
	GGUFTypeBool    uint32 = 7
	GGUFTypeString  uint32 = 8
	GGUFTypeArray   uint32 = 9
	GGUFTypeUint64  uint32 = 10
	GGUFTypeInt64   uint32 = 11
	GGUFTypeFloat64 uint32 = 12
)

type BlockQ8_0 struct {
	D float32
	Q [32]int8
}

type Tensor struct {
	Type uint32
	F32  []float32
	Q8   []BlockQ8_0
}

type GGUFHeader struct {
	Magic           [4]byte
	Version         uint32
	TensorCount     uint64
	MetadataKVCount uint64
}

type GGUFTensorInfo struct {
	Name       string
	Dimensions []uint64
	Type       uint32
	Offset     uint64
}

type GGUFParser struct {
	r         io.ReadSeeker
	Header    GGUFHeader
	Metadata  map[string]any
	Tensors   map[string]GGUFTensorInfo
	DataStart int64
}

type ReadSeekReaderAt interface {
	io.ReadSeeker
	io.ReaderAt
}

func NewGGUFParser(r io.ReadSeeker) (*GGUFParser, error) {
	p := &GGUFParser{
		r:        r,
		Metadata: make(map[string]any),
		Tensors:  make(map[string]GGUFTensorInfo),
	}
	if err := p.parse(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *GGUFParser) parse() error {
	if err := binary.Read(p.r, binary.LittleEndian, &p.Header.Magic); err != nil {
		return err
	}
	if string(p.Header.Magic[:]) != "GGUF" {
		return fmt.Errorf("invalid magic bytes, expected GGUF")
	}

	if err := binary.Read(p.r, binary.LittleEndian, &p.Header.Version); err != nil {
		return err
	}
	if p.Header.Version != 3 {
		return fmt.Errorf("unsupported GGUF version: %d", p.Header.Version)
	}

	if err := binary.Read(p.r, binary.LittleEndian, &p.Header.TensorCount); err != nil {
		return err
	}
	if err := binary.Read(p.r, binary.LittleEndian, &p.Header.MetadataKVCount); err != nil {
		return err
	}

	for range p.Header.MetadataKVCount {
		key, err := p.readString()
		if err != nil {
			return err
		}
		valType, err := p.readUint32()
		if err != nil {
			return err
		}
		val, err := p.readValue(valType)
		if err != nil {
			return fmt.Errorf("failed parsing metadata key %q: %w", key, err)
		}
		p.Metadata[key] = val
	}

	for range p.Header.TensorCount {
		name, err := p.readString()
		if err != nil {
			return err
		}
		nDims, err := p.readUint32()
		if err != nil {
			return err
		}
		dims := make([]uint64, nDims)
		for i := range nDims {
			if dims[i], err = p.readUint64(); err != nil {
				return err
			}
		}
		tType, err := p.readUint32()
		if err != nil {
			return err
		}
		offset, err := p.readUint64()
		if err != nil {
			return err
		}

		p.Tensors[name] = GGUFTensorInfo{
			Name:       name,
			Dimensions: dims,
			Type:       tType,
			Offset:     offset,
		}
	}

	alignment := uint64(32)
	if alignVal, ok := p.Metadata["general.alignment"]; ok {
		if a, ok := alignVal.(uint32); ok {
			alignment = uint64(a)
		}
	}

	currentPos, err := p.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	padding := (alignment - (uint64(currentPos) % alignment)) % alignment
	p.DataStart = currentPos + int64(padding)

	return nil
}

func (p *GGUFParser) readString() (string, error) {
	length, err := p.readUint64()
	if err != nil {
		return "", err
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(p.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func (p *GGUFParser) readUint32() (uint32, error) {
	var val uint32
	err := binary.Read(p.r, binary.LittleEndian, &val)
	return val, err
}

func (p *GGUFParser) readUint64() (uint64, error) {
	var val uint64
	err := binary.Read(p.r, binary.LittleEndian, &val)
	return val, err
}

func (p *GGUFParser) readValue(valType uint32) (any, error) {
	switch valType {
	case GGUFTypeUint8:
		var v uint8
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeInt8:
		var v int8
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeUint16:
		var v uint16
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeInt16:
		var v int16
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeUint32:
		var v uint32
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeInt32:
		var v int32
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeFloat32:
		var v float32
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeBool:
		var v byte
		binary.Read(p.r, binary.LittleEndian, &v)
		return v != 0, nil
	case GGUFTypeString:
		return p.readString()
	case GGUFTypeUint64:
		var v uint64
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeInt64:
		var v int64
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeFloat64:
		var v float64
		binary.Read(p.r, binary.LittleEndian, &v)
		return v, nil
	case GGUFTypeArray:
		arrType, err := p.readUint32()
		if err != nil {
			return nil, err
		}
		arrLen, err := p.readUint64()
		if err != nil {
			return nil, err
		}
		arr := make([]any, arrLen)
		for i := range arrLen {
			val, err := p.readValue(arrType)
			if err != nil {
				return nil, err
			}
			arr[i] = val
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("unknown GGUF value type: %d", valType)
	}
}

func f16ToF32(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32((h >> 10) & 0x1F)
	mant := uint32(h & 0x3FF)
	var bits uint32
	switch {
	case exp == 0 && mant == 0:
		bits = sign
	case exp == 0:
		exp = 1
		for mant&0x400 == 0 {
			mant <<= 1
			exp--
		}
		mant &= 0x3FF
		bits = sign | ((exp + 127 - 15) << 23) | (mant << 13)
	case exp == 31:
		bits = sign | 0x7F800000 | (mant << 13)
	default:
		bits = sign | ((exp + 127 - 15) << 23) | (mant << 13)
	}
	return math.Float32frombits(bits)
}

func toUint64(v any) uint64 {
	switch val := v.(type) {
	case uint64:
		return val
	case uint32:
		return uint64(val)
	case int64:
		return uint64(val)
	case int32:
		return uint64(val)
	case int:
		return uint64(val)
	default:
		return 0
	}
}

func tensorSizeBytes(tType uint32, dims []uint64) (uint64, error) {
	elements := uint64(1)
	for _, d := range dims {
		elements *= d
	}
	switch tType {
	case 0:
		return elements * 4, nil
	case 1, 30:
		return elements * 2, nil
	case 8:
		numBlocks := (elements + 31) / 32
		return numBlocks * 34, nil
	default:
		return 0, fmt.Errorf("unsupported tensor type: %d", tType)
	}
}

func parseLayerIndex(tensorName string) int {
	if strings.HasPrefix(tensorName, "token_embd") {
		return -1
	}
	if strings.HasPrefix(tensorName, "output_norm") {
		return 99
	}
	if strings.HasPrefix(tensorName, "blk.") {
		parts := strings.Split(tensorName, ".")
		if len(parts) > 1 {
			if idx, err := strconv.Atoi(parts[1]); err == nil {
				return idx
			}
		}
	}
	return 999
}

func (p *GGUFParser) getTensorBytes(name string) ([]byte, error) {
	return db.AiGetTensorData(name)
}

func (p *GGUFParser) GetTensor(name string) (Tensor, error) {
	tInfo, ok := p.Tensors[name]
	if !ok {
		return Tensor{}, fmt.Errorf("tensor %s not found", name)
	}

	size := uint64(1)
	for _, dim := range tInfo.Dimensions {
		size *= dim
	}

	buf, err := p.getTensorBytes(name)
	if err != nil {
		return Tensor{}, err
	}

	if tInfo.Type == 0 {
		out := make([]float32, size)
		for i := range out {
			bits := binary.LittleEndian.Uint32(buf[i*4:])
			out[i] = math.Float32frombits(bits)
		}
		return Tensor{Type: 0, F32: out}, nil
	}

	if tInfo.Type == 1 {
		out := make([]float32, size)
		for i := range out {
			h := binary.LittleEndian.Uint16(buf[i*2:])
			out[i] = f16ToF32(h)
		}
		return Tensor{Type: 0, F32: out}, nil
	}

	if tInfo.Type == 30 {
		out := make([]float32, size)
		for i := range out {
			u16 := binary.LittleEndian.Uint16(buf[i*2:])
			out[i] = math.Float32frombits(uint32(u16) << 16)
		}
		return Tensor{Type: 0, F32: out}, nil
	}

	if tInfo.Type == 8 {
		numBlocks := (size + 31) / 32
		q8Blocks := make([]BlockQ8_0, numBlocks)
		for j := uint64(0); j < numBlocks; j++ {
			blockOff := j * 34
			h := binary.LittleEndian.Uint16(buf[blockOff : blockOff+2])
			q8Blocks[j].D = f16ToF32(h)
			for k := uint64(0); k < 32; k++ {
				q8Blocks[j].Q[k] = int8(buf[blockOff+2+k])
			}
		}
		return Tensor{Type: 8, Q8: q8Blocks}, nil
	}

	return Tensor{}, fmt.Errorf("unsupported tensor type: %d", tInfo.Type)
}

func (p *GGUFParser) GetTensorF32(name string) ([]float32, error) {
	tInfo, ok := p.Tensors[name]
	if !ok {
		return nil, fmt.Errorf("tensor %s not found", name)
	}

	size := uint64(1)
	for _, dim := range tInfo.Dimensions {
		size *= dim
	}

	buf, err := p.getTensorBytes(name)
	if err != nil {
		return nil, err
	}

	if tInfo.Type == 0 {
		out := make([]float32, size)
		for i := range out {
			bits := binary.LittleEndian.Uint32(buf[i*4:])
			out[i] = math.Float32frombits(bits)
		}
		return out, nil
	}

	if tInfo.Type == 1 {
		out := make([]float32, size)
		for i := range out {
			h := binary.LittleEndian.Uint16(buf[i*2:])
			out[i] = f16ToF32(h)
		}
		return out, nil
	}

	if tInfo.Type == 30 {
		out := make([]float32, size)
		for i := range out {
			u16 := binary.LittleEndian.Uint16(buf[i*2:])
			out[i] = math.Float32frombits(uint32(u16) << 16)
		}
		return out, nil
	}

	if tInfo.Type == 8 {
		numBlocks := (size + 31) / 32
		out := make([]float32, size)
		for j := uint64(0); j < numBlocks; j++ {
			blockOff := j * 34
			h := binary.LittleEndian.Uint16(buf[blockOff : blockOff+2])
			d := f16ToF32(h)
			for k := uint64(0); k < 32; k++ {
				idx := j*32 + k
				if idx >= size {
					break
				}
				q := int8(buf[blockOff+2+k])
				out[idx] = float32(q) * d
			}
		}
		return out, nil
	}

	return nil, fmt.Errorf("unsupported tensor type: %d", tInfo.Type)
}

func (p *GGUFParser) GetTokenEmbedding(id int, hDim int) ([]float32, error) {
	tInfo, ok := p.Tensors["token_embd.weight"]
	if !ok {
		return nil, fmt.Errorf("tensor token_embd.weight not found")
	}

	dim0 := int64(tInfo.Dimensions[0])
	var vocabSize int64
	if len(tInfo.Dimensions) >= 2 {
		vocabSize = int64(tInfo.Dimensions[1])
	} else {
		totalElements := int64(1)
		for _, d := range tInfo.Dimensions {
			totalElements *= int64(d)
		}
		vocabSize = totalElements / dim0
	}

	if int64(id) >= vocabSize {
		return nil, fmt.Errorf("token ID %d out of bounds (vocab size %d)", id, vocabSize)
	}

	out := make([]float32, hDim)

	buf, err := db.AiGetTensorData("token_embd.weight")
	if err != nil {
		return nil, err
	}

	if tInfo.Type == 1 {
		start := int64(id) * dim0 * 2
		for i := range out {
			h := binary.LittleEndian.Uint16(buf[start+int64(i*2):])
			out[i] = f16ToF32(h)
		}
		return out, nil
	}
	if tInfo.Type == 30 {
		start := int64(id) * dim0 * 2
		for i := range out {
			u16 := binary.LittleEndian.Uint16(buf[start+int64(i*2):])
			out[i] = math.Float32frombits(uint32(u16) << 16)
		}
		return out, nil
	}
	if tInfo.Type == 0 {
		start := int64(id) * dim0 * 4
		for i := range out {
			bits := binary.LittleEndian.Uint32(buf[start+int64(i*4):])
			out[i] = math.Float32frombits(bits)
		}
		return out, nil
	}
	if tInfo.Type == 8 {
		numBlocksPerToken := (dim0 + 31) / 32
		start := int64(id) * numBlocksPerToken * 34
		for j := int64(0); j < numBlocksPerToken; j++ {
			blockOff := start + j*34
			h := binary.LittleEndian.Uint16(buf[blockOff : blockOff+2])
			d := f16ToF32(h)
			for k := int64(0); k < 32; k++ {
				idx := j*32 + k
				if idx >= int64(hDim) {
					break
				}
				q := int8(buf[blockOff+2+k])
				out[idx] = float32(q) * d
			}
		}
		return out, nil
	}

	return nil, fmt.Errorf("unsupported tensor type: %d", tInfo.Type)
}
