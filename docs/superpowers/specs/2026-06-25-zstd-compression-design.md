# Add zstd as a selectable compression codec

**Date:** 2026-06-25
**Status:** Approved

## Summary

The cache currently compresses backend blobs with a single, hardcoded LZ4
codec toggled by a boolean `COMPRESSION` flag. This change adds zstd as a
selectable alternative and makes the read path codec-agnostic by detecting the
codec from the blob's magic bytes. zstd typically achieves a noticeably better
compression ratio than LZ4 at comparable throughput, which reduces both backend
storage cost and network transfer time to remote backends (S3/GCS).

## Goals

- Let users choose `zstd`, `lz4`, or `none` for backend compression.
- Preserve backward compatibility: existing caches (raw LZ4 frames, no codec
  header) continue to read correctly, and switching codecs requires no
  migration or cache clear.
- No behavior change for existing deployments by default.

## Non-goals (YAGNI)

- Configurable zstd compression level (hardcode `SpeedDefault`).
- A custom codec header/magic byte (the codecs' native frame magics suffice).
- Re-compressing or migrating existing blobs to a new codec.

## Configuration

Repurpose the existing `COMPRESSION` flag/env (`-compression`,
`GOBUILDCACHE_COMPRESSION` / `COMPRESSION`) to accept a codec name while still
parsing the legacy boolean. Parsing is case-insensitive.

| Value           | Meaning                       |
|-----------------|-------------------------------|
| `zstd`          | zstd at `SpeedDefault`        |
| `lz4`           | LZ4 (current behavior)        |
| `none` / `false`| no compression                |
| `true`          | alias for `lz4` (back-compat) |
| (unset)         | default `lz4`                 |

**Default stays `lz4`** — equivalent to the current `COMPRESSION=true` default —
so existing deployments do not silently switch codec. Users opt into zstd
explicitly. An unrecognized value is a startup error.

Internally, the `compression bool` field on `CacheProg` becomes a small
`compressionAlgo` enum (`none` / `lz4` / `zstd`), threaded through
`NewCacheProg`. A parse helper converts the flag string to the enum.

## Write path (PUT)

`compressData` takes the algo and switches on it:

- `zstd` → `klauspost/compress/zstd` writer at `SpeedDefault`
- `lz4`  → existing `pierrec/lz4` writer (unchanged)
- `none` → store raw bytes (existing else-branch in `handlePut`)

Compression statistics (`compressionBytesIn` / `compressionBytesOut`) are
tracked as today, for any algo that actually compresses.

## Read path (GET) — auto-detect by magic bytes

Decompression ignores the configured codec and detects the codec from the
blob's leading bytes. Peek the first 4 bytes:

- `0x184D2204` → LZ4 frame → decompress with lz4
- `0x28B52FFD` → zstd frame → decompress with zstd
- anything else → raw / uncompressed → stream through unchanged

Consequences:

- Existing LZ4 caches keep working when configured for zstd or `none`.
- Mixed-codec caches (blobs written before/after a codec switch) work.
- No migration or cache clear needed when changing codecs.

Compressed blobs are read fully and decompressed in memory (matching the
current LZ4 read path); raw blobs continue to stream directly to the local
cache. Decompression statistics
(`decompressionBytesIn` / `decompressionBytesOut`) are tracked when a blob is
actually decompressed.

## Dependency

Add `github.com/klauspost/compress` (pure Go, cgo-free), consistent with the
current cgo-free build using `pierrec/lz4`.

## Stats & help text

- Update the `-compression` flag usage and the `COMPRESSION` env description in
  the help output to describe codec names (`none` / `lz4` / `zstd`, plus legacy
  `true`/`false`) instead of "Enable LZ4 compression".
- The compression statistics section notes which codec is active.

## Testing

- zstd round-trip; lz4 round-trip (regression).
- Magic-byte detection: an lz4 blob, a zstd blob, and raw bytes each decode
  correctly.
- Back-compat: a blob written as lz4 decodes correctly while the server is
  configured for zstd.
- `COMPRESSION` parsing: `zstd`, `lz4`, `none`, `true`, `false`, mixed case, and
  an unknown value (error).
