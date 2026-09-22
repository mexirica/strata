# Development

This guide covers the checks and code paths most relevant to internal changes.
The project uses standard Go tooling and keeps tests beside each package.

## Local Validation

Run the complete suite:

```bash
go test ./...
```

Run the same core checks as CI:

```bash
go vet ./...
go test -race -coverprofile=coverage.out ./...
mkdir -p bin
go build -trimpath -o bin/strata ./cmd/cli
```

Focused examples:

```bash
go test ./internal/node ./internal/maintenance
go test ./internal/cas ./internal/manifeststore ./internal/storage
go test -run TestName ./internal/fileservice
go test -fuzz=FuzzParseCIDBytes ./internal/cid
```

The [CI workflow](../../.github/workflows/ci.yml) runs vet, race-enabled tests
with coverage, and the CLI build on pushes and pull requests. It publishes the
binary and coverage profile as artifacts.

## Test Map

| Area | Main test package | Important behavior |
| --- | --- | --- |
| CID | [`internal/cid`](../../internal/cid) | Binary/text round trips, invalid encodings, fuzzing |
| Hashing | [`internal/hasher`](../../internal/hasher) | Algorithm selection and known digests |
| Chunking | [`internal/chunker`](../../internal/chunker) | Reconstruction, deterministic boundaries, cancellation |
| Storage | [`internal/storage`](../../internal/storage) | Persistence, atomic races, cursors, closed state, fuzzing |
| CAS | [`internal/cas`](../../internal/cas) | Concurrent puts, algorithm-aware verification, corruption |
| Manifests | [`internal/manifeststore`](../../internal/manifeststore) | Encoding, CRUD, pagination, limits, corruption, fuzzing |
| File service | [`internal/fileservice`](../../internal/fileservice) | Round trips, limits, streaming, early close |
| Maintenance | [`internal/maintenance`](../../internal/maintenance) | Shared chunks, orphan collection, dry runs, scrub issues |
| Node | [`internal/node`](../../internal/node) | Composition, locking, retrieval lifetime, close behavior |
| CLI | [`internal/cli`](../../internal/cli) | End-to-end command workflow and configuration |

Most storage tests use temporary in-memory Badger instances. Persistence and
restart scenarios use temporary directories.

## Change Checklists

### Persisted Formats

1. Read [Storage Format](storage-format.md).
2. Preserve existing vectors or add a versioned reader.
3. Add malformed, truncated, oversized, and unsupported-version cases.
4. Verify both configured hash algorithms.
5. Explain repository compatibility in the pull request.

### Storage Backend Behavior

1. Preserve atomic `PutIfNotExists` behavior under concurrency.
2. Never overwrite an existing content key with different bytes.
3. Keep cursors exclusive and prefix scans ordered.
4. Map backend closed and missing-key states consistently.
5. Exercise persistence, cancellation, and concurrent writers.

### File Operations

1. Keep large files streaming and memory bounded by chunk size.
2. Check limits before unbounded allocation or integer conversion.
3. Publish the manifest only after all chunks are durable.
4. Verify every read before exposing bytes.
5. Test cancellation and early reader close.

### Maintenance

1. Treat manifests as the only reachability roots.
2. Keep collection mutually exclusive with facade operations.
3. Make dry-run and destructive results distinguishable to callers.
4. Fail toward retaining data when reachability is uncertain.
5. Include shared-chunk and interrupted-store scenarios.

## Known Gaps

Areas where new work should begin with a design decision rather than an
implicit behavior change:

- repository format/configuration records and migrations;
- bounded-memory reachability for very large repositories;
- stable cross-page snapshots for maintenance scans;
- repair or quarantine after scrub findings;
- daemon lifecycle and remote access;
- cross-process or distributed coordination.
