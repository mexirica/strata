# Strata Internals

This directory documents the implementation contracts behind Strata. It is
intended for contributors changing storage behavior, persisted formats, or
repository maintenance. For installation and CLI usage, start with the
[project README](../../README.md).

## Guides

| Document | Scope |
| --- | --- |
| [Architecture](architecture.md) | Package boundaries, dependency direction, and repository composition |
| [Storage Format](storage-format.md) | CID and manifest encodings, Badger keys, and compatibility rules |
| [Data Flows](data-flows.md) | Store, retrieve, list, delete, garbage collection, and scrub paths |
| [Concurrency and Integrity](concurrency-and-integrity.md) | Locking, lifecycle, atomicity, verification, and failure behavior |
| [Development](development.md) | Test layout, validation commands, and change checklists |

## Core Invariants

These rules connect the documents and should remain true across changes:

1. A CID identifies bytes and includes the hash algorithm needed to verify
   those bytes.
2. A manifest is the only root that makes chunks reachable.
3. Chunk order in a manifest defines file reconstruction order.
4. Every manifest and chunk is verified before its contents are trusted.
5. Garbage collection never runs concurrently with facade operations.
6. Persisted encodings and key prefixes are compatibility boundaries.

## Current Scope

Strata is a local, single-process repository. It has no daemon, remote API,
replication, distributed lock, or repository migration framework. Planned
features should not be inferred from these documents; each guide describes
only behavior present in the current code.
