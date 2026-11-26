# Storage Format

Persisted bytes define Strata's storage format. Manifest v1 now stores ordered
CID-and-size references instead of a CID-only list. This is a direct change to
v1: development repositories containing pre-change CID-only manifests are
unsupported, with no migration, v2 reader, or compatibility mode.

## Content Identifiers

[`cid.CID`](../../internal/content/cid/cid.go) is exactly 34 bytes:

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

[`file.ManifestStore`](../../internal/file/manifest_store.go) uses one
canonical, big-endian encoding:

| Size | Field |
| ---: | --- |
| 16 bytes | Domain tag `strata-manifest\x00` |
| 2 bytes | Format version, currently `1`, unsigned 16-bit integer |
| 4 bytes | UTF-8 name length, unsigned 32-bit integer |
| variable | Name bytes |
| 8 bytes | Reconstructed file size, unsigned 64-bit integer |
| 4 bytes | Chunk count, unsigned 32-bit integer |
| `42 * count` bytes | Ordered binary CID-and-size chunk references |

Each chunk reference has the following canonical layout. Offsets are relative
to the start of the reference:

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 34 bytes | Binary chunk CID |
| 34 | 8 bytes | Chunk size in bytes, unsigned 64-bit big-endian integer |

Chunk offsets within the reconstructed file are not persisted. They are derived
cumulatively from validated chunk sizes.

For a name of length $N$ and $C$ chunks, the encoded size is:

$$
34 + N + 42C
$$

The manifest CID hashes this entire encoding, including the domain tag,
version, name, size, and ordered CID-and-size references. Consequently:

- renaming a file changes its manifest CID;
- reordering chunks changes its manifest CID;
- changing a declared chunk size changes its manifest CID;
- the same raw bytes cannot accidentally share an identity with a manifest;
- chunk hashes remain hashes of raw chunk bytes without a domain prefix.

Structural validation on both reads and writes requires version 1, a nonempty
valid UTF-8 name, valid chunk CIDs, and a nonnegative reconstructed file size
that fits in a signed 64-bit integer. Every chunk size must be greater than
zero. The cumulative sum must not exceed the signed 64-bit maximum and must
exactly equal the declared file size. Empty files have no chunks.

Truncated references, invalid CIDs, encoded-length mismatches, zero-sized
chunks, arithmetic overflow, and reconstructed-size mismatches are rejected.

Sequential retrieval verifies each chunk's hash and actual length before writing
its bytes to the output stream. An individual reference-size mismatch returns
`file.ErrChunkSizeMismatch` while reading the stream; reconstructed-total
mismatches use `file.ErrManifestSizeMismatch`. Scrub reports individual mismatches
as `IssueChunkSizeMismatch`, with both manifest and chunk CIDs and an error wrapping
`file.ErrChunkSizeMismatch`. Total mismatches remain `IssueManifestSizeMismatch`.

Configured name-length, chunk-count, and encoded-manifest-size limits apply
only to writes. Existing manifests are decoded using structural validation,
independently of current write limits.

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

[`storage.Badger`](../../internal/storage/badger.go) enables synchronous
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
1 MiB target, and 4 MiB maximum. Objects using the current manifest layout
remain readable independently of the current write hash preference because
their CIDs carry their algorithm. Pre-change CID-only manifests are unsupported.

The repository metadata is the source of truth for chunking. Initialization
selects the algorithm and sizes, and every later open constructs the chunker
from those persisted values. A repeated initialization request with different
values is rejected with both the repository and requested settings.

Hash algorithms follow a different rule. Every chunk and manifest CID embeds
its hash algorithm, allowing reads to select the correct verifier independently
of the current write preference. The repository metadata therefore does not
persist a list of hash algorithms supported by the build. Persisted chunking
and CID algorithm identifiers are validated against the algorithms implemented
by the running Strata version.

Repository and manifest format versions are persisted format settings; this
development change updates the manifest v1 layout without a version bump.
Chunking is a persisted policy for new writes. The data directory and default
hash algorithm are invocation preferences, while maximum file size, chunk
count, and name length are write safety limits. Those limits reject new writes
but are not applied while decoding existing manifests; structural validation
of the encoded manifest remains mandatory.

## Manifest Test Vectors

The following blocks contain the complete manifest encodings in hexadecimal.
Whitespace separates lines for readability and is not persisted.
[`TestManifestStore_DocumentedVectors`](../../internal/file/manifest_store_test.go)
checks every block against an independent golden fixture. For valid vectors it
also checks the production encoder byte for byte, twice; malformed vectors are
stored under their recomputed BLAKE3 CID so decoding, rather than a hash
mismatch, must reject them with `file.ErrInvalidManifest`.

The two-chunk fixture uses structurally valid BLAKE3 CIDs with synthetic digests
(`01` or `02` followed by 31 zero bytes). It tests manifest encoding only and
does not represent chunks whose contents can be retrieved from CAS. Reference
offsets in that fixture are 42 and 84; the size fields start at 76 and 118.

| Vector | Bytes | Expected result |
| --- | ---: | --- |
| `empty-file` | 39 | Version 1, name `a.txt`, size 0, no references |
| `two-chunks` | 126 | Version 1, name `test.pdf`, size 1024, ordered sizes 256 and 768 |
| `truncated-reference` | 125 | Reject: second reference is missing the last byte of its size |
| `zero-chunk-size` | 126 | Reject: first reference declares zero bytes |
| `chunk-size-overflow` | 126 | Reject: first reference declares $2^{63}$ bytes, above the signed 64-bit maximum |
| `cumulative-size-overflow` | 126 | Reject: sizes $2^{63}-1$ and 1 overflow the allowed sum |
| `reconstructed-size-mismatch` | 126 | Reject: sizes 256 and 769 sum to 1025, not the declared 1024 |

### empty-file

```text
7374726174612d6d616e696665737400000100000005612e7478740000000000
00000000000000
```

### two-chunks

```text
7374726174612d6d616e696665737400000100000008746573742e7064660000
000000000400000000021e200100000000000000000000000000000000000000
00000000000000000000000000000000000001001e2002000000000000000000
000000000000000000000000000000000000000000000000000000000300
```

### truncated-reference

```text
7374726174612d6d616e696665737400000100000008746573742e7064660000
000000000400000000021e200100000000000000000000000000000000000000
00000000000000000000000000000000000001001e2002000000000000000000
0000000000000000000000000000000000000000000000000000000003
```

### zero-chunk-size

```text
7374726174612d6d616e696665737400000100000008746573742e7064660000
000000000400000000021e200100000000000000000000000000000000000000
00000000000000000000000000000000000000001e2002000000000000000000
000000000000000000000000000000000000000000000000000000000300
```

### chunk-size-overflow

```text
7374726174612d6d616e696665737400000100000008746573742e7064660000
000000000400000000021e200100000000000000000000000000000000000000
00000000000000000000000080000000000000001e2002000000000000000000
000000000000000000000000000000000000000000000000000000000300
```

### cumulative-size-overflow

```text
7374726174612d6d616e696665737400000100000008746573742e7064667fff
ffffffffffff000000021e200100000000000000000000000000000000000000
0000000000000000000000007fffffffffffffff1e2002000000000000000000
000000000000000000000000000000000000000000000000000000000001
```

### reconstructed-size-mismatch

```text
7374726174612d6d616e696665737400000100000008746573742e7064660000
000000000400000000021e200100000000000000000000000000000000000000
00000000000000000000000000000000000001001e2002000000000000000000
000000000000000000000000000000000000000000000000000000000301
```

## Format Change Checklist

Before changing a prefix, algorithm code, domain tag, field width, byte order,
or validation rule:

1. Define whether old repositories remain readable. This manifest v1 change
   explicitly excludes pre-change development manifests.
2. Add golden vectors for the supported layout and verify byte-identical
   output when encoding the same manifest twice.
3. Document field widths, byte order, and validation rules. This change keeps
   version 1 and adds no migration or compatibility reader.
4. Verify ordered CID-and-size round trips without persisted chunk offsets.
5. Test corrupt, truncated, zero-sized, overflowing, size-mismatched, and
   oversized inputs.
