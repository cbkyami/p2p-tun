package plugin

import (
	"bytes"
	"fmt"
	"io"

	"github.com/pierrec/lz4/v4"
)

type Compression struct {
	minSize int
}

func NewCompression(level, minSize int) *Compression {
	if minSize < 0 {
		minSize = 128
	}
	return &Compression{minSize: minSize}
}

func (c *Compression) Compress(data []byte) ([]byte, bool) {
	if len(data) < c.minSize {
		return data, false
	}

	var buf bytes.Buffer
	w := lz4.NewWriter(&buf)
	w.Apply(lz4.CompressionLevelOption(lz4.Level1))
	_, err := w.Write(data)
	w.Close()
	if err != nil {
		return data, false
	}

	compressed := buf.Bytes()
	if len(compressed) >= len(data) {
		return data, false
	}

	return compressed, true
}

const maxDecompressedSize = 262144

func (c *Compression) Decompress(data []byte) ([]byte, error) {
	r := lz4.NewReader(bytes.NewReader(data))

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(r, maxDecompressedSize+1)); err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}

	if buf.Len() > maxDecompressedSize {
		return nil, fmt.Errorf("decompress: output exceeds max size %d", maxDecompressedSize)
	}

	return buf.Bytes(), nil
}
