# Development

This guide covers the checks and code paths most relevant to internal changes.
The project requires Go 1.27.1 or newer, uses standard Go tooling, and keeps
tests beside each package.

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
go test ./internal/repository/... ./internal/maintenance
go test ./internal/content/cas ./internal/file ./internal/storage
go test -run TestFileService_StoreAndRetrieve_Roundtrip ./internal/file
go test -fuzz=FuzzParseCIDBytes -fuzztime=10s ./internal/content/cid
```

The [CI workflow](../../.github/workflows/ci.yml) runs vet, race-enabled tests
with coverage, and the CLI build on pushes and pull requests. It publishes the
binary and coverage profile as artifacts.

## Test Map

| Area | Main test package | Important behavior |
| --- | --- | --- |
| CID | [`internal/content/cid`](../../internal/content/cid) | Binary/text round trips, invalid encodings, fuzzing |
| Hashing | [`internal/content/hasher`](../../internal/content/hasher) | Algorithm selection and known digests |
| Chunking | [`internal/content/chunker`](../../internal/content/chunker) | Reconstruction, deterministic boundaries, cancellation |
| Storage | [`internal/storage`](../../internal/storage) | Persistence, atomic races, cursors, closed state, fuzzing |
| CAS | [`internal/content/cas`](../../internal/content/cas) | Concurrent puts, algorithm-aware verification, corruption |
| Manifests | [`internal/file`](../../internal/file) | Encoding, CRUD, pagination, limits, corruption, fuzzing |
| File service | [`internal/file`](../../internal/file) | Round trips, limits, streaming, early close |
| Maintenance | [`internal/maintenance`](../../internal/maintenance) | Shared chunks, orphan collection, dry runs, scrub issues |
| Repository | [`internal/repository`](../../internal/repository) | Composition, locking, retrieval lifetime, close behavior |
| Metadata | [`internal/repository/metadata`](../../internal/repository/metadata) | Empty-only initialization, persisted chunking, compatibility |
| CLI | [`internal/cli`](../../internal/cli) | End-to-end command workflow and configuration |

Most storage tests use temporary in-memory Badger instances. Persistence and
restart scenarios use temporary directories.

## Test Style

Use table-driven tests whenever an operation has multiple input or expected-result
scenarios. Define a `tests` slice with descriptive case names and run each case
with `t.Run(tc.name, ...)`, keeping the execution and assertions in the subtest.
Include expected errors in the table and compare them with `errors.Is`.

Create mutable fixtures inside each subtest so cases remain independent. Only
share fixtures when cases do not change their state. Keep tables local to their
test and avoid generic test runners or one-row tables.

Preserve sequential integration tests when later steps depend on earlier ones,
including lifecycle, persistence, pagination, and concurrency workflows. Keep
single-scenario tests and fuzz targets in their natural form. Setup, assertion,
and concurrency loops are not scenario tables.

## Change Checklists

### Readability and Technical Contracts

Use private constants for binary widths and technical limits, deriving offsets
where the relation between fields matters. Keep golden vectors literal and
independent of production constants. Do not rename every zero, loop increment,
or already meaningful default.

Document non-obvious method contracts: borrowed buffer lifetime, verification
before emission, ownership and mandatory Close, validation priority, atomicity,
and partial results on failure. Keep GoDoc concise; explain longer flows in
these guides. Refactoring must preserve errors, validation boundaries, output,
persisted bytes, and lock lifetimes.

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

1. In the current manifest-only format, treat manifests as reachability roots.
   In the planned snapshot format, traverse every retained snapshot through its
   directory trees and manifests before considering chunks unreachable.
2. Keep collection mutually exclusive with facade operations.
3. Make dry-run and destructive results distinguishable to callers.
4. Fail toward retaining data when reachability is uncertain.
5. Include shared-chunk, shared-subtree, removed-snapshot, and interrupted-store
   scenarios as the corresponding formats are implemented.

## Known Gaps

Areas where new work should begin with a design decision rather than an
implicit behavior change:

- repository format migrations;
- bounded-memory reachability for very large repositories;
- stable cross-page snapshots for maintenance scans;
- repair or quarantine after scrub findings;
- local daemon lifecycle and Unix-socket access;
- cross-process coordination through the repository daemon.
- immutable snapshot and content-addressed directory-tree formats;
- snapshot retention, selectors, structural diff, and reachability roots;
- read-only FUSE views for selected and retained snapshots;
- local snapshot metadata with remote S3-compatible chunks.
