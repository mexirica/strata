# Storage Format

Persisted bytes are part of Strata's compatibility contract. Changes to this
document's encodings require an explicit migration or a versioned read path.

## Content Identifiers

[`cid.CID`](../../internal/cid/cid.go) is exactly 34 bytes:

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 1 | Algorithm code: `0x12` for SHA-256 or `0x1e` for BLAKE3 |
| 1 | 1 | Digest length, currently always `32` |
| 2 | 32 | Hash digest |

The text form is lowercase hexadecimal over all 34 bytes, producing 68
characters. It is multihash-style, but it is not a complete multiformats CID.
The algorithm byte lets reads choose the correct verifier independently of the
current write configuration.

## Manifest Encoding

[`manifeststore`](../../internal/manifeststore/manifest_store.go) uses one
canonical, big-endian encoding:

| Size | Field |
| ---: | --- |
| 16 bytes | Domain tag `strata-manifest\x00` |
| 2 bytes | Format version, currently `1` |
| 4 bytes | UTF-8 name length |
| variable | Name bytes |
| 8 bytes | Reconstructed file size |
| 4 bytes | Chunk count |
| `34 * count` bytes | Ordered binary chunk CIDs |

For a name of length $N$ and $C$ chunks, the encoded size is:

$$
34 + N + 34C
$$

The manifest CID hashes this entire encoding, including the domain tag,
version, name, size, and ordered chunk list. Consequently:

- renaming a file changes its manifest CID;
- reordering chunks changes its manifest CID;
- the same raw bytes cannot accidentally share an identity with a manifest;
- chunk hashes remain hashes of raw chunk bytes without a domain prefix.

Manifest validation requires version 1, a nonempty valid UTF-8 name within the
configured byte limit, a nonnegative size, valid chunk CIDs, and at least one
chunk for a nonempty file. Empty files have no chunks.

## Repository Metadata

Each initialized repository contains one immutable JSON document at the
reserved ASCII key `repository:metadata`. Version 1 has the following canonical
field order and shape:

```json
{
	"format_version": 1,
	"created_at": "2026-09-24T12:00:00Z",
	"app_version": "v0.1.0",
	"chunking": {
		"algorithm": "fastcdc-v1.0.0",
		"min_size": 262144,
		"normal_size": 1048576,
		"max_size": 4194304
	},
	"manifest_format_version": 1
}
```

| Field | Meaning |
| --- | --- |
| `format_version` | Version of the repository metadata schema, currently `1` |
| `created_at` | Repository creation time in UTC |
| `app_version` | Strata build version that initialized the repository |
| `chunking.algorithm` | Content-defined chunking algorithm used for writes |
| `chunking.min_size` | Minimum chunk size in bytes |
| `chunking.normal_size` | Target chunk size in bytes |
| `chunking.max_size` | Maximum chunk size in bytes |
| `manifest_format_version` | Manifest encoding version required by the repository |

Encoding uses Go's `encoding/json` representation of this fixed structure.
Therefore, the same metadata value produces the same bytes. Creation timestamps
are normalized to UTC before encoding.

Metadata is written with `PutIfNotExists` and is never updated automatically.
Opening a repository validates its schema version, required fields, manifest
version, chunking algorithm, and chunk-size constraints before constructing any
service that can read or write repository objects. A future repository format
version, malformed document, or unsupported persisted configuration prevents
the repository from opening.

When the key is absent, metadata is created only if the database contains no
other keys. A nonempty database without `repository:metadata` is treated as a
legacy repository and rejected. Strata does not silently adopt or migrate such
a repository.

## Badger Keys

Logical objects are separated by ASCII prefixes followed by binary CIDs:

| Object | Key construction | Key size |
| --- | --- | ---: |
| Repository metadata | `repository:metadata` | 19 bytes |
| Chunk | `chunk:` + 34 CID bytes | 40 bytes |
| Manifest | `manifest:` + 34 CID bytes | 43 bytes |

Prefix scans are lexicographically ordered by the binary CID suffix. Pagination
cursors contain the last CID returned and are exclusive on the next page.

## Write Semantics

[`storage.BadgerStore`](../../internal/storage/badger.go) enables synchronous
writes. Each get, put, conditional put, delete, or page read owns a Badger
transaction. `PutIfNotExists` retries optimistic conflicts; an existing key
with different bytes returns a content-mismatch error instead of overwriting
content addressed data.

There is no transaction spanning all chunks and their manifest. This ordering
is intentional:

1. write each chunk idempotently;
2. write the manifest only after every chunk succeeds;
3. allow garbage collection to reclaim chunks left by an interrupted store.

## Configuration Compatibility

The default write policy is BLAKE3 with FastCDC sizes of 256 KiB minimum,
1 MiB target, and 4 MiB maximum. Existing objects remain readable because
their CIDs carry their algorithm and manifests carry their format version.

The repository metadata is the compatibility boundary for chunking. On reopen,
the configured chunking algorithm and sizes must exactly match the persisted
values. Strata rejects a mismatch before exposing read or write operations, so
chunk boundaries cannot change silently within a repository.

Hash algorithms follow a different rule. Every chunk and manifest CID embeds
its hash algorithm, allowing reads to select the correct verifier independently
of the current write preference. The repository metadata therefore does not
persist a list of hash algorithms supported by the build. Persisted chunking
and CID algorithm identifiers are validated against the algorithms implemented
by the running Strata version.

## Format Change Checklist

Before changing a prefix, algorithm code, domain tag, field width, byte order,
or validation rule:

1. Define whether old repositories remain readable.
2. Add golden vectors for both old and new formats.
3. Version the format before writing new bytes.
4. Document migration and rollback behavior.
5. Test corrupt, truncated, and oversized inputs.
