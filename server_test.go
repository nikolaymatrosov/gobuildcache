package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardartoul/gobuildcache/pkg/backends"
	"github.com/richardartoul/gobuildcache/pkg/locking"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1024 * 1024, "1.00 MB"},
		{1536 * 1024, "1.50 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
		{1024*1024*1024*2 + 1024*1024*512, "2.50 GB"},
		{1024 * 1024 * 1024 * 1024, "1.00 TB"},
		{1024*1024*1024*1024*3 + 1024*1024*1024*716, "3.70 TB"},
	}

	for _, tt := range tests {
		result := formatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("formatBytes(%d) = %s, expected %s", tt.bytes, result, tt.expected)
		}
	}
}

// createTestCacheProg creates a CacheProg for testing with the specified readOnly setting.
func createTestCacheProg(t *testing.T, readOnly bool) (*CacheProg, string) {
	t.Helper()

	// Create a temporary directory for the cache
	cacheDir, err := os.MkdirTemp("", "gobuildcache-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}

	backend := backends.NewNoop()
	locker := locking.NewNoOpGroup()

	cp, err := NewCacheProg(backend, locker, cacheDir, false, false, compressNone, readOnly)
	if err != nil {
		os.RemoveAll(cacheDir)
		t.Fatalf("Failed to create CacheProg: %v", err)
	}

	return cp, cacheDir
}

func TestReadOnlyMode_SkipsPut(t *testing.T) {
	cp, cacheDir := createTestCacheProg(t, true) // readOnly = true
	defer os.RemoveAll(cacheDir)

	// Create a PUT request
	req := &Request{
		ID:       1,
		Command:  CmdPut,
		ActionID: []byte("test-action-id-12345678"),
		OutputID: []byte("test-output-id-12345678"),
		Body:     strings.NewReader("test body content"),
		BodySize: 17,
	}

	// Execute the PUT
	resp, err := cp.handlePut(req)
	if err != nil {
		t.Fatalf("handlePut returned error: %v", err)
	}

	// Verify response is successful (no error)
	if resp.Err != "" {
		t.Errorf("Expected no error in response, got: %s", resp.Err)
	}

	// Verify skippedPuts counter was incremented (backend write skipped)
	if cp.skippedPuts.Load() != 1 {
		t.Errorf("Expected skippedPuts to be 1, got: %d", cp.skippedPuts.Load())
	}

	// Verify putCount WAS incremented (local cache write still happens)
	if cp.putCount.Load() != 1 {
		t.Errorf("Expected putCount to be 1, got: %d", cp.putCount.Load())
	}

	// Verify local cache was written to (DiskPath should be set)
	if resp.DiskPath == "" {
		t.Error("Expected DiskPath to be set (local cache should be written)")
	}

	// Verify backendBytesWritten is 0 (no backend write)
	if cp.backendBytesWritten.Load() != 0 {
		t.Errorf("Expected backendBytesWritten to be 0, got: %d", cp.backendBytesWritten.Load())
	}
}

func TestReadOnlyMode_Disabled_AllowsPut(t *testing.T) {
	cp, cacheDir := createTestCacheProg(t, false) // readOnly = false
	defer os.RemoveAll(cacheDir)

	// Create a PUT request
	req := &Request{
		ID:       1,
		Command:  CmdPut,
		ActionID: []byte("test-action-id-12345678"),
		OutputID: []byte("test-output-id-12345678"),
		Body:     strings.NewReader("test body content"),
		BodySize: 17,
	}

	// Execute the PUT
	resp, err := cp.handlePut(req)
	if err != nil {
		t.Fatalf("handlePut returned error: %v", err)
	}

	// Verify response is successful (no error)
	if resp.Err != "" {
		t.Errorf("Expected no error in response, got: %s", resp.Err)
	}

	// Verify skippedPuts was NOT incremented
	if cp.skippedPuts.Load() != 0 {
		t.Errorf("Expected skippedPuts to be 0, got: %d", cp.skippedPuts.Load())
	}

	// Verify putCount WAS incremented
	if cp.putCount.Load() != 1 {
		t.Errorf("Expected putCount to be 1, got: %d", cp.putCount.Load())
	}
}

// memBackend is a minimal in-memory Backend implementation for tests. Unlike
// the Noop backend (which always returns a miss), it actually stores the bytes
// written by handlePut and serves them back on Get, so a CacheProg with a cold
// local cache is forced through the backend-hit read path (Peek + detectCodec +
// decompress).
type memBackend struct {
	mu    sync.Mutex
	store map[string]memBlob
}

type memBlob struct {
	outputID []byte
	data     []byte
	putTime  time.Time
}

func newMemBackend() *memBackend {
	return &memBackend{store: make(map[string]memBlob)}
}

func (m *memBackend) Put(actionID, outputID []byte, body io.Reader, bodySize int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[string(actionID)] = memBlob{outputID: outputID, data: data, putTime: time.Now()}
	return nil
}

func (m *memBackend) Get(actionID []byte) ([]byte, io.ReadCloser, int64, *time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	blob, ok := m.store[string(actionID)]
	if !ok {
		return nil, nil, 0, nil, true, nil
	}
	pt := blob.putTime
	return blob.outputID, io.NopCloser(bytes.NewReader(blob.data)), int64(len(blob.data)), &pt, false, nil
}

func (m *memBackend) Close() error { return nil }

func (m *memBackend) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = make(map[string]memBlob)
	return nil
}

// newCacheProgWithBackend builds a CacheProg using the given backend, codec, and
// a fresh empty local cache directory (registered for cleanup). Sharing one
// backend between two such instances lets a PUT on one be served from the
// backend by a GET on the other (whose local cache starts cold).
func newCacheProgWithBackend(t *testing.T, backend backends.Backend, algo compressionAlgo) *CacheProg {
	t.Helper()

	cacheDir, err := os.MkdirTemp("", "gobuildcache-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(cacheDir) })

	cp, err := NewCacheProg(backend, locking.NewNoOpGroup(), cacheDir, false, false, algo, false)
	if err != nil {
		t.Fatalf("Failed to create CacheProg: %v", err)
	}
	return cp
}

// TestGetDecompressesBackendBlob exercises the refactored handleGet read path
// (bufio.Peek + detectCodec, then stream-through for raw or read-all+decompress
// for detected blobs) by doing a real PUT-then-GET round trip through an
// in-memory backend with a cold local cache on the GET side.
func TestGetDecompressesBackendBlob(t *testing.T) {
	// Reasonably compressible payload so the compressed blob differs from the
	// original and the codecs actually engage.
	original := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 256)

	cases := []struct {
		name         string
		algo         compressionAlgo
		expectDecomp bool // whether the GET path should decompress (vs stream raw through)
	}{
		{"lz4", compressLZ4, true},
		{"zstd", compressZstd, true},
		{"none", compressNone, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newMemBackend()

			// PUT side: writes to the backend with the configured codec.
			putProg := newCacheProgWithBackend(t, backend, tc.algo)
			actionID := []byte("decompress-action-id-0001")
			outputID := []byte("decompress-output-id-0001")

			putResp, err := putProg.handlePut(&Request{
				ID:       1,
				Command:  CmdPut,
				ActionID: actionID,
				OutputID: outputID,
				Body:     bytes.NewReader(original),
				BodySize: int64(len(original)),
			})
			if err != nil {
				t.Fatalf("handlePut returned error: %v", err)
			}
			if putResp.Err != "" {
				t.Fatalf("handlePut response error: %s", putResp.Err)
			}

			// GET side: a separate CacheProg with a fresh/cold local cache,
			// sharing the backend, so the GET misses locally and fetches from
			// the backend, exercising detection + decompression.
			getProg := newCacheProgWithBackend(t, backend, tc.algo)
			getResp, err := getProg.handleGet(&Request{
				ID:       2,
				Command:  CmdGet,
				ActionID: actionID,
			})
			if err != nil {
				t.Fatalf("handleGet returned error: %v", err)
			}
			if getResp.Miss {
				t.Fatalf("expected backend hit, got miss")
			}

			// The bytes written to the local cache (served to Go) must equal the
			// original uncompressed payload.
			if getResp.Size != int64(len(original)) {
				t.Errorf("response size = %d, want %d", getResp.Size, len(original))
			}
			got, err := os.ReadFile(getResp.DiskPath)
			if err != nil {
				t.Fatalf("failed to read cached file at %s: %v", getResp.DiskPath, err)
			}
			if !bytes.Equal(got, original) {
				t.Errorf("cached bytes do not match original (got %d bytes, want %d)", len(got), len(original))
			}

			// It must be a backend hit (not a local hit).
			if getProg.backendCacheHits.Load() != 1 {
				t.Errorf("backendCacheHits = %d, want 1", getProg.backendCacheHits.Load())
			}

			// Decompression counters: set only when a compressed blob was
			// detected and decompressed; the raw (compressNone) blob streams
			// through untouched.
			decompIn := getProg.decompressionBytesIn.Load()
			decompOut := getProg.decompressionBytesOut.Load()
			if tc.expectDecomp {
				if decompIn <= 0 {
					t.Errorf("decompressionBytesIn = %d, want > 0", decompIn)
				}
				if decompOut != int64(len(original)) {
					t.Errorf("decompressionBytesOut = %d, want %d", decompOut, len(original))
				}
			} else {
				if decompIn != 0 || decompOut != 0 {
					t.Errorf("raw blob should not record decompression stats, got in=%d out=%d", decompIn, decompOut)
				}
			}
		})
	}
}

func TestReadOnlyMode_MultipleSkippedPuts(t *testing.T) {
	cp, cacheDir := createTestCacheProg(t, true) // readOnly = true
	defer os.RemoveAll(cacheDir)

	// Execute multiple PUTs with unique action IDs
	for i := 0; i < 5; i++ {
		actionID := make([]byte, 24)
		copy(actionID, []byte("test-action-id-1234567"))
		actionID[23] = byte('0' + i)

		req := &Request{
			ID:       int64(i),
			Command:  CmdPut,
			ActionID: actionID,
			OutputID: []byte("test-output-id-12345678"),
			Body:     strings.NewReader("test body"),
			BodySize: 9,
		}

		_, err := cp.handlePut(req)
		if err != nil {
			t.Fatalf("handlePut %d returned error: %v", i, err)
		}
	}

	// Verify all 5 backend writes were skipped
	if cp.skippedPuts.Load() != 5 {
		t.Errorf("Expected skippedPuts to be 5, got: %d", cp.skippedPuts.Load())
	}

	// Verify putCount is 5 (local cache writes still happen)
	if cp.putCount.Load() != 5 {
		t.Errorf("Expected putCount to be 5, got: %d", cp.putCount.Load())
	}
}
