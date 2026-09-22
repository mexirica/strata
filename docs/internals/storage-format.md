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

## Badger Keys

Logical objects are separated by ASCII prefixes followed by binary CIDs:

| Object | Key construction | Key size |
| --- | --- | ---: |
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

The repository does not persist the chunking configuration used by earlier
writes. Operators should keep hashing and chunking settings stable. A change
does not make old data unreadable, but it changes future boundaries and reduces
deduplication between old and new files.

## Format Change Checklist

Before changing a prefix, algorithm code, domain tag, field width, byte order,
or validation rule:

1. Define whether old repositories remain readable.
2. Add golden vectors for both old and new formats.
3. Version the format before writing new bytes.
4. Document migration and rollback behavior.
5. Test corrupt, truncated, and oversized inputs.
