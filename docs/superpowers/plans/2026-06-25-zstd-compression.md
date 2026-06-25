# Selectable zstd Compression Codec Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add zstd as a selectable backend compression codec alongside the existing LZ4, selected via the repurposed `COMPRESSION` flag, with read-side codec auto-detection by magic bytes.

**Architecture:** A new `compression.go` module owns a `compressionAlgo` enum, flag parsing, and codec-aware compress/decompress with magic-byte detection. `server.go` (`CacheProg`) carries the enum instead of a bool and detects the codec on read. `main.go` parses the `COMPRESSION` flag string into the enum. Existing LZ4 caches keep working because decompression is driven by the blob's own frame magic, not by configuration.

**Tech Stack:** Go 1.25, `github.com/pierrec/lz4/v4` (existing), `github.com/klauspost/compress/zstd` (new, pure-Go cgo-free).

**Why the bool→enum change is one task:** `compressData`/`decompressData` move from `server.go` to `compression.go` and change signature, and `CacheProg.compression` changes type. These cannot compile in isolation — the package only builds once `compression.go`, `server.go`, and `main.go` are all updated. Task 2 therefore makes the whole change behind one green commit, with the failing tests as the TDD "red" state.

---

## File Structure

- **Create** `compression.go` — codec enum, `parseCompressionAlgo`, `compressData`, `decompressData`, `detectCodec`, magic-byte constants. Single responsibility: compression codec logic.
- **Create** `compression_test.go` — unit tests for the above.
- **Modify** `server.go` — replace `compression bool` with the enum across `CacheProg`, `NewCacheProg`, `handlePut`, `handleGet`, and stats printing. Remove the old `compressData`/`decompressData` (moved to `compression.go`) and the now-unused lz4 import.
- **Modify** `main.go` — `COMPRESSION` flag becomes a codec-name string, parsed into the enum.
- **Modify** `go.mod` / `go.sum` — add `klauspost/compress`.

---

## Task 1: Add the zstd dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Fetch the dependency**

Run:
```bash
go get github.com/klauspost/compress/zstd && go mod tidy
```
Expected: `go.mod`/`go.sum` gain a `github.com/klauspost/compress` entry. The package still builds (`go build ./...` succeeds — nothing uses it yet).

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add github.com/klauspost/compress dependency for zstd"
```

---

## Task 2: Codec module + rewire CacheProg and flag (one atomic change)

**Files:**
- Create: `compression.go`
- Test: `compression_test.go`
- Modify: `server.go`, `main.go`

- [ ] **Step 1: Write the failing tests**

Create `compression_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to confirm the red state**

Run:
```bash
go test . -run 'TestParseCompressionAlgo|TestCompressDecompressRoundTrip|TestCompressNoneReturnsRaw|TestDecompressDataRaw|TestDetectCodec' 2>&1 | head -10
```
Expected: compile failure — `undefined: compressionAlgo`, `undefined: parseCompressionAlgo`, etc. (This is the failing state; it stays red until every edit in this task is done.)

- [ ] **Step 3: Create `compression.go`**

```go
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
```

- [ ] **Step 4: Remove the old codec functions and lz4 import from `server.go`**

Delete the entire `compressData` and `decompressData` functions at the end of `server.go` (the block starting with the comment `// compressData compresses data using LZ4 ...` through the closing brace of `decompressData`).

Then, in the import block of `server.go`, delete the line:
```go
	"github.com/pierrec/lz4/v4"
```

- [ ] **Step 5: Change the `CacheProg` field type**

In `server.go`, in the `CacheProg` struct, change:
```go
	compression bool
```
to:
```go
	compression compressionAlgo
```

- [ ] **Step 6: Change the `NewCacheProg` parameter type**

In `server.go`, in the `NewCacheProg` signature, change:
```go
	compression bool,
```
to:
```go
	compression compressionAlgo,
```
(The `compression: compression,` field assignment in the struct literal stays unchanged.)

- [ ] **Step 7: Update the PUT compression gate**

In `server.go` `handlePut`, change:
```go
		if cp.compression && req.BodySize > 0 {
			compressStart := time.Now()
			compressed, err := compressData(bodyData)
```
to:
```go
		if cp.compression != compressNone && req.BodySize > 0 {
			compressStart := time.Now()
			compressed, err := compressData(bodyData, cp.compression)
```

- [ ] **Step 8: Replace the GET decompression block with magic-byte detection**

In `server.go` `handleGet`, replace this block:
```go
		// Backend hit - decompress if needed, then write to local cache with metadata
		defer body.Close()

		var dataToCache io.Reader
		var actualSize int64

		if cp.compression && size > 0 {
			// Read compressed data from backend
			compressedData, err := io.ReadAll(body)
			if err != nil {
				return nil, fmt.Errorf("failed to read compressed data from backend: %w", err)
			}

			// Decompress data
			decompressStart := time.Now()
			decompressed, err := decompressData(compressedData)
			cp.latencyTracker.Record("get_decompression", time.Since(decompressStart))

			if err != nil {
				return nil, fmt.Errorf("failed to decompress data: %w", err)
			}

			// Track decompression statistics
			cp.decompressionBytesIn.Add(size)
			cp.decompressionBytesOut.Add(int64(len(decompressed)))

			dataToCache = bytes.NewReader(decompressed)
			actualSize = int64(len(decompressed))
		} else {
			dataToCache = body
			actualSize = size
		}
```
with:
```go
		// Backend hit - detect the codec from the blob's magic bytes and
		// decompress if needed, then write to local cache with metadata.
		// Detection (not configuration) drives this, so existing LZ4 caches and
		// mixed-codec caches read correctly regardless of the configured codec.
		defer body.Close()

		var dataToCache io.Reader
		var actualSize int64

		if size > 0 {
			// Peek the leading bytes to detect the codec without consuming the
			// stream. Uncompressed blobs stream straight through; compressed
			// blobs are read fully and decompressed in memory.
			br := bufio.NewReader(body)
			magic, _ := br.Peek(4) // returns fewer than 4 bytes near EOF; detectCodec handles that
			if detectCodec(magic) == compressNone {
				dataToCache = br
				actualSize = size
			} else {
				compressedData, err := io.ReadAll(br)
				if err != nil {
					return nil, fmt.Errorf("failed to read compressed data from backend: %w", err)
				}

				decompressStart := time.Now()
				decompressed, _, err := decompressData(compressedData)
				cp.latencyTracker.Record("get_decompression", time.Since(decompressStart))

				if err != nil {
					return nil, fmt.Errorf("failed to decompress data: %w", err)
				}

				cp.decompressionBytesIn.Add(size)
				cp.decompressionBytesOut.Add(int64(len(decompressed)))

				dataToCache = bytes.NewReader(decompressed)
				actualSize = int64(len(decompressed))
			}
		} else {
			dataToCache = body
			actualSize = size
		}
```

- [ ] **Step 9: Update the stats-print guard and header**

In `server.go`, in the stats printing section, change:
```go
		// Print compression statistics if compression is enabled
		if cp.compression {
			fmt.Fprintf(os.Stderr, "\nCompression statistics:\n")
```
to:
```go
		// Print compression statistics if compression is enabled
		if cp.compression != compressNone {
			fmt.Fprintf(os.Stderr, "\nCompression statistics (codec: %s):\n", cp.compression)
```

- [ ] **Step 10: Change the `main.go` flag variable type**

In `main.go`, in the package-level `var (...)` block, change:
```go
	compression  bool
```
to:
```go
	compression  string
```

- [ ] **Step 11: Change the `main.go` default to a codec name**

In `main.go` `runServerCommand`, change:
```go
		compressionDefault  = getEnvBoolWithPrefix("COMPRESSION", true)
```
to:
```go
		compressionDefault  = getEnvWithPrefix("COMPRESSION", "lz4")
```

- [ ] **Step 12: Register the flag as a string**

In `main.go`, change:
```go
	serverFlags.BoolVar(&compression, "compression", compressionDefault, "Enable LZ4 compression for backend storage (env: COMPRESSION)")
```
to:
```go
	serverFlags.StringVar(&compression, "compression", compressionDefault, "Backend compression codec: none, lz4, zstd; legacy true/false accepted (env: COMPRESSION)")
```

- [ ] **Step 13: Update the env help text**

In `main.go`, in the `serverFlags.Usage` function, change:
```go
		fmt.Fprintf(os.Stderr, "  COMPRESSION      Enable LZ4 compression (true/false)\n")
```
to:
```go
		fmt.Fprintf(os.Stderr, "  COMPRESSION      Backend compression codec: none, lz4, zstd (legacy true/false)\n")
```

- [ ] **Step 14: Parse the codec and pass it to `NewCacheProg`**

In `main.go` `runServerCommand`, change:
```go
	prog, err := NewCacheProg(backend, lockingGroup, cacheDir, debug, printStats, compression, readOnly)
```
to:
```go
	compAlgo, err := parseCompressionAlgo(compression)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid compression setting: %v\n", err)
		os.Exit(1)
	}

	prog, err := NewCacheProg(backend, lockingGroup, cacheDir, debug, printStats, compAlgo, readOnly)
```

- [ ] **Step 15: Build and run the full test suite (green state)**

Run:
```bash
go build ./... && go test ./... 
```
Expected: build succeeds; all tests PASS, including the codec tests from Step 1.

- [ ] **Step 16: Smoke-test codec validation**

Run:
```bash
go run . -compression=bogus -backend=disk 2>&1 | head -2
```
Expected: prints `Invalid compression setting: unknown compression algorithm "bogus" (valid: none, lz4, zstd)` and exits non-zero.

- [ ] **Step 17: Commit**

```bash
git add compression.go compression_test.go server.go main.go
git commit -m "feat: select backend compression codec (none/lz4/zstd) with read-side detection"
```

---

## Notes for the implementer

- **Default is `lz4`** — existing deployments (`COMPRESSION` unset or `true`) keep their current behavior. zstd is strictly opt-in via `COMPRESSION=zstd`.
- **No migration needed.** Read-side detection means a cache written with LZ4 still reads after switching to `COMPRESSION=zstd`, and vice versa.
- The whole bool→enum change lands in Task 2's single commit because the type change spans three files and cannot compile partially.
