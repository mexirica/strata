package file_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/content/hasher"
	"github.com/mexirica/strata/internal/file"
	"github.com/mexirica/strata/internal/storage"
)

func setupManifestStore(t testing.TB) (*file.ManifestStore, storage.ObjectStorage) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to init in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := file.NewManifestStore(db, hasher.NewBlake3Hasher(), file.ManifestConfig{
		MaxManifestSize: 1024 * 1024,
		MaxNameBytes:    1024,
		MaxChunks:       1024,
		MaxPageSize:     100,
	})
	if err != nil {
		t.Fatalf("failed to init manifest store: %v", err)
	}
	return store, db
}

func TestManifestStore_CRUD(t *testing.T) {
	store, _ := setupManifestStore(t)
	ctx := context.Background()

	var d1 [32]byte
	d1[0] = 1
	var d2 [32]byte
	d2[0] = 2
	cid1, err := cid.NewCID(cid.AlgBlake3, d1)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}
	cid2, err := cid.NewCID(cid.AlgBlake3, d2)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	manifest := file.FileManifest{
		Version: file.CurrentVersion,
		Name:    "test.pdf",
		Size:    1024,
		Chunks:  []file.ChunkReference{{CID: cid1, Size: 256}, {CID: cid2, Size: 768}},
	}

	manifestCID, err := store.Put(ctx, manifest)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	duplicateCID, err := store.Put(ctx, manifest)
	if err != nil || duplicateCID != manifestCID {
		t.Fatalf("duplicate Put must return the same CID: cid=%v err=%v", duplicateCID, err)
	}

	got, err := store.Get(ctx, manifestCID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if got.Version != manifest.Version || got.Name != manifest.Name || got.Size != manifest.Size {
		t.Fatalf("mismatched manifest: got %+v, want %+v", got, manifest)
	}
	if len(got.Chunks) != 2 || got.Chunks[0] != manifest.Chunks[0] || got.Chunks[1] != manifest.Chunks[1] {
		t.Fatalf("mismatched chunks: got %+v, want %+v", got.Chunks, manifest.Chunks)
	}

	list, next, err := store.ListManifests(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListManifests failed: %v", err)
	}
	if len(list) != 1 || list[0].CID != manifestCID || next != nil {
		t.Fatalf("expected 1 manifest in list, got %+v", list)
	}

	if err := store.Delete(ctx, manifestCID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.Get(ctx, manifestCID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestManifestStore_DocumentedVectors(t *testing.T) {
	const domainHex = "7374726174612d6d616e696665737400"
	const nameHeaderHex = domainHex + "000100000008746573742e706466"
	const headerHex = nameHeaderHex + "000000000000040000000002"
	firstHex := "1e2001" + strings.Repeat("00", 31)
	secondHex := "1e2002" + strings.Repeat("00", 31)
	firstCID, err := cid.NewCID(cid.AlgBlake3, [32]byte{1})
	if err != nil {
		t.Fatalf("create first fixture CID: %v", err)
	}
	secondCID, err := cid.NewCID(cid.AlgBlake3, [32]byte{2})
	if err != nil {
		t.Fatalf("create second fixture CID: %v", err)
	}
	tests := []struct {
		name     string
		hex      string
		manifest file.FileManifest
		wantErr  error
	}{
		{
			name:     "empty-file",
			hex:      domainHex + "000100000005612e747874000000000000000000000000",
			manifest: file.FileManifest{Version: 1, Name: "a.txt"},
		},
		{
			name: "two-chunks",
			hex:  headerHex + firstHex + "0000000000000100" + secondHex + "0000000000000300",
			manifest: file.FileManifest{
				Version: 1, Name: "test.pdf", Size: 1024,
				Chunks: []file.ChunkReference{{CID: firstCID, Size: 256}, {CID: secondCID, Size: 768}},
			},
		},
		{
			name:    "truncated-reference",
			hex:     headerHex + firstHex + "0000000000000100" + secondHex + "00000000000003",
			wantErr: file.ErrInvalidManifest,
		},
		{
			name:    "zero-chunk-size",
			hex:     headerHex + firstHex + "0000000000000000" + secondHex + "0000000000000300",
			wantErr: file.ErrInvalidManifest,
		},
		{
			name:    "chunk-size-overflow",
			hex:     headerHex + firstHex + "8000000000000000" + secondHex + "0000000000000300",
			wantErr: file.ErrInvalidManifest,
		},
		{
			name:    "cumulative-size-overflow",
			hex:     nameHeaderHex + "7fffffffffffffff00000002" + firstHex + "7fffffffffffffff" + secondHex + "0000000000000001",
			wantErr: file.ErrInvalidManifest,
		},
		{
			name:    "reconstructed-size-mismatch",
			hex:     headerHex + firstHex + "0000000000000100" + secondHex + "0000000000000301",
			wantErr: file.ErrInvalidManifest,
		},
	}
	document, err := os.ReadFile("../../docs/internals/storage-format.md")
	if err != nil {
		t.Fatalf("read storage-format documentation: %v", err)
	}
	_, vectors, found := strings.Cut(string(document), "\n## Manifest Test Vectors\n")
	if !found {
		t.Fatal("missing documented manifest vector section")
	}
	vectors, _, _ = strings.Cut(vectors, "\n## ")
	if strings.Count(vectors, "\n### ") != len(tests) || strings.Count(vectors, "```text\n") != len(tests) {
		t.Fatal("every documented vector must have exactly one named section, hex block, and golden test")
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, section, found := strings.Cut(vectors, "\n### "+tc.name+"\n")
			if !found {
				t.Fatal("missing documented vector")
			}
			section, _, _ = strings.Cut(section, "\n### ")
			_, block, found := strings.Cut(section, "```text\n")
			if !found {
				t.Fatal("missing hexadecimal block")
			}
			block, _, found = strings.Cut(block, "\n```")
			if !found {
				t.Fatal("unterminated hexadecimal block")
			}
			if documentedHex := strings.Join(strings.Fields(block), ""); documentedHex != tc.hex {
				t.Fatalf("documented bytes differ from golden fixture:\n got %s\nwant %s", documentedHex, tc.hex)
			}
			golden, err := hex.DecodeString(tc.hex)
			if err != nil {
				t.Fatalf("decode golden bytes: %v", err)
			}
			store, raw := setupManifestStore(t)
			ctx := context.Background()
			manifestCID := hasher.NewBlake3Hasher().Hash(golden)
			key := append([]byte("manifest:"), manifestCID.Bytes()...)
			if tc.wantErr != nil {
				if err := raw.Put(ctx, key, golden); err != nil {
					t.Fatalf("store rehashed malformed vector: %v", err)
				}
				if _, err := store.Get(ctx, manifestCID); !errors.Is(err, tc.wantErr) {
					t.Fatalf("Get returned %v, want %v", err, tc.wantErr)
				}
				return
			}
			for attempt := 0; attempt < 2; attempt++ {
				encodedCID, err := store.Put(ctx, tc.manifest)
				if err != nil {
					t.Fatalf("encode manifest: %v", err)
				}
				encoded, err := raw.Get(ctx, append([]byte("manifest:"), encodedCID.Bytes()...))
				if err != nil {
					t.Fatalf("read encoded bytes: %v", err)
				}
				if encodedCID != manifestCID || !bytes.Equal(encoded, golden) {
					t.Fatalf("encoding %d differs from documented golden vector: %x", attempt, encoded)
				}
				decoded, err := store.Get(ctx, manifestCID)
				if err != nil {
					t.Fatalf("decode golden manifest: %v", err)
				}
				if decoded.Version != tc.manifest.Version || decoded.Name != tc.manifest.Name || decoded.Size != tc.manifest.Size || !slices.Equal(decoded.Chunks, tc.manifest.Chunks) {
					t.Fatalf("decoded manifest differs: got %+v, want %+v", decoded, tc.manifest)
				}
				if err := store.Delete(ctx, manifestCID); err != nil {
					t.Fatalf("delete before re-encoding: %v", err)
				}
			}
		})
	}
}

func TestManifestStore_RejectsInvalidChunkReferences(t *testing.T) {
	store, _ := setupManifestStore(t)
	chunkCID := hasher.NewBlake3Hasher().Hash([]byte("chunk"))
	for _, test := range []struct {
		name   string
		size   int64
		chunks []file.ChunkReference
	}{
		{"invalid CID", 1, []file.ChunkReference{{Size: 1}}},
		{"zero size", 0, []file.ChunkReference{{CID: chunkCID}}},
		{"chunk size overflow", 1, []file.ChunkReference{{CID: chunkCID, Size: math.MaxUint64}}},
		{"cumulative overflow", math.MaxInt64, []file.ChunkReference{{CID: chunkCID, Size: math.MaxInt64}, {CID: chunkCID, Size: 1}}},
		{"size mismatch", 6, []file.ChunkReference{{CID: chunkCID, Size: 5}}},
		{"missing references", 1, nil},
		{"negative file size", -1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.Put(context.Background(), file.FileManifest{
				Version: file.CurrentVersion,
				Name:    "invalid.bin",
				Size:    test.size,
				Chunks:  test.chunks,
			})
			if !errors.Is(err, file.ErrInvalidManifest) {
				t.Fatalf("Put returned %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestManifestStore_RejectsMalformedEncodedReferences(t *testing.T) {
	store, raw := setupManifestStore(t)
	ctx := context.Background()
	h := hasher.NewBlake3Hasher()
	chunkCID := h.Hash([]byte("chunk"))
	manifest := file.FileManifest{
		Version: file.CurrentVersion,
		Name:    "data",
		Size:    10,
		Chunks:  []file.ChunkReference{{CID: chunkCID, Size: 4}, {CID: chunkCID, Size: 6}},
	}
	manifestCID, err := store.Put(ctx, manifest)
	if err != nil {
		t.Fatalf("Put seed manifest: %v", err)
	}
	valid, err := raw.Get(ctx, append([]byte("manifest:"), manifestCID.Bytes()...))
	if err != nil {
		t.Fatalf("read seed bytes: %v", err)
	}
	fileSizeOffset := 22 + len(manifest.Name)
	countOffset := fileSizeOffset + 8
	referenceOffset := countOffset + 4
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"invalid CID", func(data []byte) []byte { data[referenceOffset] = 0; return data }},
		{"zero size", func(data []byte) []byte {
			binary.BigEndian.PutUint64(data[referenceOffset+34:], 0)
			return data
		}},
		{"chunk size overflow", func(data []byte) []byte {
			binary.BigEndian.PutUint64(data[referenceOffset+34:], math.MaxUint64)
			return data
		}},
		{"cumulative overflow", func(data []byte) []byte {
			binary.BigEndian.PutUint64(data[fileSizeOffset:], math.MaxInt64)
			binary.BigEndian.PutUint64(data[referenceOffset+34:], math.MaxInt64)
			binary.BigEndian.PutUint64(data[referenceOffset+42+34:], 1)
			return data
		}},
		{"file size overflow", func(data []byte) []byte {
			binary.BigEndian.PutUint64(data[fileSizeOffset:], uint64(math.MaxInt64)+1)
			return data
		}},
		{"size mismatch", func(data []byte) []byte {
			binary.BigEndian.PutUint64(data[referenceOffset+34:], 5)
			return data
		}},
		{"invalid count", func(data []byte) []byte {
			binary.BigEndian.PutUint32(data[countOffset:], math.MaxUint32)
			return data
		}},
		{"truncated CID", func(data []byte) []byte { return data[:referenceOffset+33] }},
		{"truncated size", func(data []byte) []byte { return data[:len(data)-1] }},
		{"trailing bytes", func(data []byte) []byte { return append(data, 0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := test.mutate(bytes.Clone(valid))
			valueCID := h.Hash(data)
			if err := raw.Put(ctx, append([]byte("manifest:"), valueCID.Bytes()...), data); err != nil {
				t.Fatalf("store malformed bytes: %v", err)
			}
			if _, err := store.Get(ctx, valueCID); !errors.Is(err, file.ErrInvalidManifest) {
				t.Fatalf("Get returned %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestManifestStore_ContentAddressedIdentity(t *testing.T) {
	store, _ := setupManifestStore(t)
	manifest := file.FileManifest{Version: file.CurrentVersion, Name: "a.txt", Size: 0}
	first, err := store.Put(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	manifest.Name = "b.txt"
	second, err := store.Put(context.Background(), manifest)
	if err != nil {
		t.Fatalf("renamed Put failed: %v", err)
	}
	if first == second {
		t.Fatal("renaming must change the manifest CID")
	}
	if got := first.String(); got != "1e205f18ae5d7af989d7d68c157bee10cc8bfe503bbb4f5deae151725aa3d68a9779" {
		t.Fatalf("canonical CID changed: got %s", got)
	}
}

func TestManifestStore_ReadIgnoresCurrentWriteLimits(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("NewInMemoryBadger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := hasher.NewBlake3Hasher()
	writer, err := file.NewManifestStore(db, h, file.ManifestConfig{
		MaxManifestSize: 1024,
		MaxNameBytes:    128,
		MaxChunks:       8,
		MaxPageSize:     100,
	})
	if err != nil {
		t.Fatalf("NewManifestStore writer: %v", err)
	}
	chunkCID := h.Hash([]byte("chunk"))
	manifest := file.FileManifest{
		Version: file.CurrentVersion,
		Name:    "existing-long-name.bin",
		Size:    10,
		Chunks:  []file.ChunkReference{{CID: chunkCID, Size: 5}, {CID: chunkCID, Size: 5}},
	}
	manifestCID, err := writer.Put(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Put existing manifest: %v", err)
	}

	reader, err := file.NewManifestStore(db, h, file.ManifestConfig{
		MaxManifestSize: len("strata-manifest\x00") + 19,
		MaxNameBytes:    4,
		MaxChunks:       1,
		MaxPageSize:     100,
	})
	if err != nil {
		t.Fatalf("NewManifestStore reader: %v", err)
	}
	got, err := reader.Get(context.Background(), manifestCID)
	if err != nil {
		t.Fatalf("Get existing manifest with lower write limits: %v", err)
	}
	if got.Name != manifest.Name || len(got.Chunks) != len(manifest.Chunks) {
		t.Fatalf("Get returned %+v, want %+v", got, manifest)
	}
	if _, err := reader.Put(context.Background(), manifest); !errors.Is(err, file.ErrInvalidManifest) {
		t.Fatalf("Put over current write limits returned %v, want ErrInvalidManifest", err)
	}
}

func TestManifestStore_Pagination(t *testing.T) {
	store, _ := setupManifestStore(t)
	ctx := context.Background()
	for _, name := range []string{"one", "two", "three"} {
		if _, err := store.Put(ctx, file.FileManifest{Version: file.CurrentVersion, Name: name}); err != nil {
			t.Fatalf("Put(%s) failed: %v", name, err)
		}
	}
	first, cursor, err := store.ListManifests(ctx, nil, 2)
	if err != nil {
		t.Fatalf("first page failed: %v", err)
	}
	if len(first) != 2 || cursor == nil {
		t.Fatalf("expected full first page and cursor, got len=%d cursor=%v", len(first), cursor)
	}
	second, cursor, err := store.ListManifests(ctx, cursor, 2)
	if err != nil {
		t.Fatalf("second page failed: %v", err)
	}
	if len(second) != 1 || cursor != nil {
		t.Fatalf("expected final one-item page, got len=%d cursor=%v", len(second), cursor)
	}
}

func TestManifestStore_DetectsCorruption(t *testing.T) {
	store, raw := setupManifestStore(t)
	ctx := context.Background()
	valueCID, err := store.Put(ctx, file.FileManifest{Version: file.CurrentVersion, Name: "data"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	key := append([]byte("manifest:"), valueCID.Bytes()...)
	if err := raw.Put(ctx, key, []byte("tampered")); err != nil {
		t.Fatalf("tamper failed: %v", err)
	}
	if _, err := store.Get(ctx, valueCID); !errors.Is(err, file.ErrCorruptedManifest) {
		t.Fatalf("expected ErrCorruptedManifest, got %v", err)
	}
}

func FuzzManifestDecode(f *testing.F) {
	store, raw := setupManifestStore(f)
	manifestCID, err := store.Put(context.Background(), file.FileManifest{
		Version: file.CurrentVersion,
		Name:    "seed",
	})
	if err != nil {
		f.Fatalf("seed Put failed: %v", err)
	}
	valid, err := raw.Get(context.Background(), append([]byte("manifest:"), manifestCID.Bytes()...))
	if err != nil {
		f.Fatalf("seed Get failed: %v", err)
	}
	f.Add(valid)
	f.Add([]byte("strata-manifest\x00\x00\x01\xff\xff\xff\xff"))
	f.Add([]byte("strata-manifest\x00\x00\x01\x00\x00\x00\x01a\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"))

	h := hasher.NewBlake3Hasher()
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2*1024*1024 {
			t.Skip()
		}
		valueCID := h.Hash(data)
		key := append([]byte("manifest:"), valueCID.Bytes()...)
		if err := raw.Put(context.Background(), key, data); err != nil {
			t.Fatalf("raw Put failed: %v", err)
		}
		manifest, err := store.Get(context.Background(), valueCID)
		if err == nil && manifest.Version != file.CurrentVersion {
			t.Fatalf("decoded unsupported version %d", manifest.Version)
		}
	})
}
