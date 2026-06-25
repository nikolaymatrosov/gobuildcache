package main

import (
	"bytes"
	"testing"
)

func TestParseCompressionAlgo(t *testing.T) {
	cases := []struct {
		in      string
		want    compressionAlgo
		wantErr bool
	}{
		{"zstd", compressZstd, false},
		{"ZSTD", compressZstd, false},
		{"lz4", compressLZ4, false},
		{"LZ4", compressLZ4, false},
		{"true", compressLZ4, false},
		{"none", compressNone, false},
		{"false", compressNone, false},
		{" zstd ", compressZstd, false},
		{"garbage", compressNone, true},
	}
	for _, c := range cases {
		got, err := parseCompressionAlgo(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseCompressionAlgo(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCompressionAlgo(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseCompressionAlgo(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCompressDecompressRoundTrip(t *testing.T) {
	original := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 100)
	for _, algo := range []compressionAlgo{compressLZ4, compressZstd} {
		compressed, err := compressData(original, algo)
		if err != nil {
			t.Fatalf("compressData(%v): %v", algo, err)
		}
		if bytes.Equal(compressed, original) {
			t.Errorf("compressData(%v): output equals input (not compressed)", algo)
		}
		decompressed, detected, err := decompressData(compressed)
		if err != nil {
			t.Fatalf("decompressData after %v: %v", algo, err)
		}
		if detected != algo {
			t.Errorf("decompressData detected %v, want %v", detected, algo)
		}
		if !bytes.Equal(decompressed, original) {
			t.Errorf("round trip with %v did not preserve data", algo)
		}
	}
}

func TestCompressNoneReturnsRaw(t *testing.T) {
	data := []byte("hello world")
	out, err := compressData(data, compressNone)
	if err != nil {
		t.Fatalf("compressData(none): %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("compressData(none) altered data")
	}
}

func TestDecompressDataRaw(t *testing.T) {
	data := []byte("not compressed at all")
	out, detected, err := decompressData(data)
	if err != nil {
		t.Fatalf("decompressData(raw): %v", err)
	}
	if detected != compressNone {
		t.Errorf("detected %v for raw data, want compressNone", detected)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("decompressData(raw) altered data")
	}
}

func TestDetectCodec(t *testing.T) {
	lz4Blob, err := compressData([]byte("abc"), compressLZ4)
	if err != nil {
		t.Fatal(err)
	}
	zstdBlob, err := compressData([]byte("abc"), compressZstd)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
		want compressionAlgo
	}{
		{"lz4", lz4Blob, compressLZ4},
		{"zstd", zstdBlob, compressZstd},
		{"raw", []byte("plain text"), compressNone},
		{"short", []byte{0x04, 0x22}, compressNone},
		{"empty", []byte{}, compressNone},
	}
	for _, c := range cases {
		if got := detectCodec(c.data); got != c.want {
			t.Errorf("detectCodec(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
