package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// compressionAlgo identifies which codec is used for backend blobs.
type compressionAlgo int

const (
	compressNone compressionAlgo = iota
	compressLZ4
	compressZstd
)

// String returns the codec name used in flags and stats output.
func (a compressionAlgo) String() string {
	switch a {
	case compressLZ4:
		return "lz4"
	case compressZstd:
		return "zstd"
	default:
		return "none"
	}
}

// Frame magic bytes as they appear at the start of a compressed blob
// (little-endian on the wire).
var (
	lz4Magic  = []byte{0x04, 0x22, 0x4D, 0x18} // LZ4 frame magic 0x184D2204
	zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD} // zstd frame magic 0xFD2FB528
)

// parseCompressionAlgo converts a COMPRESSION flag/env value into a codec.
// Codec names (none/lz4/zstd) and the legacy boolean (true=lz4, false=none)
// are accepted, case-insensitively. Unknown values are an error.
func parseCompressionAlgo(s string) (compressionAlgo, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "false":
		return compressNone, nil
	case "lz4", "true":
		return compressLZ4, nil
	case "zstd":
		return compressZstd, nil
	default:
		return compressNone, fmt.Errorf("unknown compression algorithm %q (valid: none, lz4, zstd)", s)
	}
}

// detectCodec identifies a blob's codec from its leading magic bytes.
// Data shorter than 4 bytes or without a known magic is treated as raw.
func detectCodec(data []byte) compressionAlgo {
	if len(data) >= 4 {
		if bytes.Equal(data[:4], lz4Magic) {
			return compressLZ4
		}
		if bytes.Equal(data[:4], zstdMagic) {
			return compressZstd
		}
	}
	return compressNone
}

// compressData compresses data with the given codec. compressNone returns the
// input unchanged.
func compressData(data []byte, algo compressionAlgo) ([]byte, error) {
	switch algo {
	case compressZstd:
		var buf bytes.Buffer
		w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd compressor: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			w.Close()
			return nil, fmt.Errorf("failed to write to zstd compressor: %w", err)
		}
		if err := w.Close(); err != nil {
			return nil, fmt.Errorf("failed to close zstd compressor: %w", err)
		}
		return buf.Bytes(), nil
	case compressLZ4:
		var buf bytes.Buffer
		w := lz4.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			w.Close()
			return nil, fmt.Errorf("failed to write to LZ4 compressor: %w", err)
		}
		if err := w.Close(); err != nil {
			return nil, fmt.Errorf("failed to close LZ4 compressor: %w", err)
		}
		return buf.Bytes(), nil
	default:
		return data, nil
	}
}

// decompressData detects the codec from magic bytes and decompresses. It
// returns the data unchanged (with compressNone) when no known compression
// magic is present, so the configured codec never has to match what was
// written.
func decompressData(data []byte) ([]byte, compressionAlgo, error) {
	switch detectCodec(data) {
	case compressLZ4:
		r := lz4.NewReader(bytes.NewReader(data))
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			return nil, compressLZ4, fmt.Errorf("failed to decompress LZ4 data: %w", err)
		}
		return buf.Bytes(), compressLZ4, nil
	case compressZstd:
		r, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, compressZstd, fmt.Errorf("failed to create zstd decompressor: %w", err)
		}
		defer r.Close()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			return nil, compressZstd, fmt.Errorf("failed to decompress zstd data: %w", err)
		}
		return buf.Bytes(), compressZstd, nil
	default:
		return data, compressNone, nil
	}
}
