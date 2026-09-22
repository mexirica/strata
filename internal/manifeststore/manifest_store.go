// Package manifeststore persists canonical content-addressed file manifests.
package manifeststore

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/storage"
)

const CurrentVersion uint16 = 1

var (
	ErrInvalidConfig      = errors.New("invalid manifest store config")
	ErrInvalidManifest    = errors.New("invalid manifest")
	ErrUnsupportedVersion = errors.New("unsupported manifest version")
	ErrCorruptedManifest  = errors.New("corrupted manifest")
)

var (
	manifestDomain = []byte("strata-manifest\x00")
	manifestPrefix = []byte("manifest:")
)

type Config struct {
	MaxManifestSize int
	MaxNameBytes    int
	MaxChunks       int
	MaxPageSize     int
}

type ManifestStore struct {
	storage storage.ObjectStorage
	hasher  hasher.Hasher
	config  Config
}

type FileManifest struct {
	Version uint16
	Name    string
	Size    int64
	Chunks  []cid.CID
}

type StoredManifest struct {
	CID      cid.CID
	Manifest FileManifest
}

func NewManifestStore(store storage.ObjectStorage, defaultHasher hasher.Hasher, config Config) (*ManifestStore, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: storage is nil", ErrInvalidConfig)
	}
	if defaultHasher == nil {
		return nil, fmt.Errorf("%w: hasher is nil", ErrInvalidConfig)
	}
	if config.MaxManifestSize <= len(manifestDomain)+18 || config.MaxNameBytes <= 0 || config.MaxChunks <= 0 || config.MaxPageSize <= 0 {
		return nil, fmt.Errorf("%w: all limits must be greater than zero", ErrInvalidConfig)
	}
	return &ManifestStore{storage: store, hasher: defaultHasher, config: config}, nil
}

func getKey(valueCID cid.CID) []byte {
	return append(append([]byte(nil), manifestPrefix...), valueCID.Bytes()...)
}

func (s *ManifestStore) Put(ctx context.Context, manifest FileManifest) (cid.CID, error) {
	data, err := s.encode(manifest)
	if err != nil {
		return cid.CID{}, err
	}
	valueCID := s.hasher.Hash(data)
	if _, err := s.storage.PutIfNotExists(ctx, getKey(valueCID), data); err != nil {
		if errors.Is(err, storage.ErrContentMismatch) {
			return cid.CID{}, fmt.Errorf("%w: %w", ErrCorruptedManifest, err)
		}
		return cid.CID{}, err
	}
	return valueCID, nil
}

func (s *ManifestStore) Get(ctx context.Context, valueCID cid.CID) (FileManifest, error) {
	if !valueCID.IsValid() {
		return FileManifest{}, fmt.Errorf("%w: invalid CID", ErrInvalidManifest)
	}
	data, err := s.storage.Get(ctx, getKey(valueCID))
	if err != nil {
		return FileManifest{}, err
	}
	return s.decodeVerified(valueCID, data)
}

func (s *ManifestStore) Delete(ctx context.Context, valueCID cid.CID) error {
	if !valueCID.IsValid() {
		return fmt.Errorf("%w: invalid CID", ErrInvalidManifest)
	}
	return s.storage.Delete(ctx, getKey(valueCID))
}

func (s *ManifestStore) ListManifests(ctx context.Context, after *cid.CID, limit int) ([]StoredManifest, *cid.CID, error) {
	if limit <= 0 || limit > s.config.MaxPageSize {
		return nil, nil, fmt.Errorf("%w: page size must be between 1 and %d", ErrInvalidManifest, s.config.MaxPageSize)
	}
	var afterKey []byte
	if after != nil {
		if !after.IsValid() {
			return nil, nil, fmt.Errorf("%w: invalid cursor CID", ErrInvalidManifest)
		}
		afterKey = getKey(*after)
	}
	objects, nextKey, err := s.storage.ListPage(ctx, manifestPrefix, afterKey, limit)
	if err != nil {
		return nil, nil, err
	}
	manifests := make([]StoredManifest, 0, len(objects))
	for _, object := range objects {
		valueCID, err := cidFromKey(object.Key)
		if err != nil {
			return nil, nil, err
		}
		manifest, err := s.decodeVerified(valueCID, object.Value)
		if err != nil {
			return nil, nil, err
		}
		manifests = append(manifests, StoredManifest{CID: valueCID, Manifest: manifest})
	}
	if len(nextKey) == 0 {
		return manifests, nil, nil
	}
	next, err := cidFromKey(nextKey)
	if err != nil {
		return nil, nil, err
	}
	return manifests, &next, nil
}

func (s *ManifestStore) ListManifestCIDs(ctx context.Context, after *cid.CID, limit int) ([]cid.CID, *cid.CID, error) {
	if limit <= 0 || limit > s.config.MaxPageSize {
		return nil, nil, fmt.Errorf("%w: page size must be between 1 and %d", ErrInvalidManifest, s.config.MaxPageSize)
	}
	var afterKey []byte
	if after != nil {
		if !after.IsValid() {
			return nil, nil, fmt.Errorf("%w: invalid cursor CID", ErrInvalidManifest)
		}
		afterKey = getKey(*after)
	}
	keys, nextKey, err := s.storage.ListKeysPage(ctx, manifestPrefix, afterKey, limit)
	if err != nil {
		return nil, nil, err
	}
	manifestCIDs := make([]cid.CID, 0, len(keys))
	for _, key := range keys {
		manifestCID, err := cidFromKey(key)
		if err != nil {
			return nil, nil, err
		}
		manifestCIDs = append(manifestCIDs, manifestCID)
	}
	if len(nextKey) == 0 {
		return manifestCIDs, nil, nil
	}
	next, err := cidFromKey(nextKey)
	if err != nil {
		return nil, nil, err
	}
	return manifestCIDs, &next, nil
}

func (s *ManifestStore) decodeVerified(valueCID cid.CID, data []byte) (FileManifest, error) {
	verifier, err := hasher.ForAlgorithm(valueCID.Algorithm())
	if err != nil {
		return FileManifest{}, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	if verifier.Hash(data) != valueCID {
		return FileManifest{}, ErrCorruptedManifest
	}
	return s.decode(data)
}

func (s *ManifestStore) encode(manifest FileManifest) ([]byte, error) {
	if err := s.validate(manifest); err != nil {
		return nil, err
	}
	total := int64(len(manifestDomain)) + 2 + 4 + int64(len(manifest.Name)) + 8 + 4 + int64(len(manifest.Chunks))*34
	if total > int64(s.config.MaxManifestSize) {
		return nil, fmt.Errorf("%w: encoded size %d exceeds %d", ErrInvalidManifest, total, s.config.MaxManifestSize)
	}
	data := make([]byte, int(total))
	offset := copy(data, manifestDomain)
	binary.BigEndian.PutUint16(data[offset:], manifest.Version)
	offset += 2
	binary.BigEndian.PutUint32(data[offset:], uint32(len(manifest.Name)))
	offset += 4
	offset += copy(data[offset:], manifest.Name)
	binary.BigEndian.PutUint64(data[offset:], uint64(manifest.Size))
	offset += 8
	binary.BigEndian.PutUint32(data[offset:], uint32(len(manifest.Chunks)))
	offset += 4
	for _, chunkCID := range manifest.Chunks {
		offset += copy(data[offset:], chunkCID.Bytes())
	}
	return data, nil
}

func (s *ManifestStore) decode(data []byte) (FileManifest, error) {
	minimum := len(manifestDomain) + 2 + 4 + 8 + 4
	if len(data) < minimum || len(data) > s.config.MaxManifestSize {
		return FileManifest{}, fmt.Errorf("%w: encoded size %d", ErrInvalidManifest, len(data))
	}
	if string(data[:len(manifestDomain)]) != string(manifestDomain) {
		return FileManifest{}, fmt.Errorf("%w: invalid domain", ErrInvalidManifest)
	}
	offset := len(manifestDomain)
	version := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if version != CurrentVersion {
		return FileManifest{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
	}
	nameLength := uint64(binary.BigEndian.Uint32(data[offset:]))
	offset += 4
	if nameLength == 0 || nameLength > uint64(s.config.MaxNameBytes) || nameLength > uint64(len(data)-offset) {
		return FileManifest{}, fmt.Errorf("%w: invalid name length %d", ErrInvalidManifest, nameLength)
	}
	nameEnd := offset + int(nameLength)
	nameBytes := data[offset:nameEnd]
	if !utf8.Valid(nameBytes) {
		return FileManifest{}, fmt.Errorf("%w: name is not valid UTF-8", ErrInvalidManifest)
	}
	offset = nameEnd
	if len(data)-offset < 12 {
		return FileManifest{}, fmt.Errorf("%w: truncated size or chunk count", ErrInvalidManifest)
	}
	size := binary.BigEndian.Uint64(data[offset:])
	offset += 8
	if size > math.MaxInt64 {
		return FileManifest{}, fmt.Errorf("%w: file size overflows int64", ErrInvalidManifest)
	}
	chunkCount := uint64(binary.BigEndian.Uint32(data[offset:]))
	offset += 4
	if chunkCount > uint64(s.config.MaxChunks) {
		return FileManifest{}, fmt.Errorf("%w: chunk count %d exceeds %d", ErrInvalidManifest, chunkCount, s.config.MaxChunks)
	}
	expected := uint64(offset) + chunkCount*34
	if expected != uint64(len(data)) {
		return FileManifest{}, fmt.Errorf("%w: encoded length mismatch", ErrInvalidManifest)
	}
	chunks := make([]cid.CID, int(chunkCount))
	for index := range chunks {
		chunkCID, err := cid.ParseCIDBytes(data[offset : offset+34])
		if err != nil {
			return FileManifest{}, fmt.Errorf("%w: chunk %d: %w", ErrInvalidManifest, index, err)
		}
		chunks[index] = chunkCID
		offset += 34
	}
	manifest := FileManifest{Version: version, Name: string(nameBytes), Size: int64(size), Chunks: chunks}
	if err := s.validate(manifest); err != nil {
		return FileManifest{}, err
	}
	return manifest, nil
}

func (s *ManifestStore) validate(manifest FileManifest) error {
	if manifest.Version != CurrentVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, manifest.Version)
	}
	if len(manifest.Name) == 0 || len(manifest.Name) > s.config.MaxNameBytes || !utf8.ValidString(manifest.Name) {
		return fmt.Errorf("%w: invalid name", ErrInvalidManifest)
	}
	if manifest.Size < 0 {
		return fmt.Errorf("%w: negative file size", ErrInvalidManifest)
	}
	if len(manifest.Chunks) > s.config.MaxChunks {
		return fmt.Errorf("%w: too many chunks", ErrInvalidManifest)
	}
	if manifest.Size > 0 && len(manifest.Chunks) == 0 {
		return fmt.Errorf("%w: non-empty file has no chunks", ErrInvalidManifest)
	}
	for index, chunkCID := range manifest.Chunks {
		if !chunkCID.IsValid() {
			return fmt.Errorf("%w: chunk %d has invalid CID", ErrInvalidManifest, index)
		}
	}
	return nil
}

func cidFromKey(key []byte) (cid.CID, error) {
	if len(key) != len(manifestPrefix)+34 || string(key[:len(manifestPrefix)]) != string(manifestPrefix) {
		return cid.CID{}, fmt.Errorf("%w: invalid storage key", ErrCorruptedManifest)
	}
	valueCID, err := cid.ParseCIDBytes(key[len(manifestPrefix):])
	if err != nil {
		return cid.CID{}, fmt.Errorf("%w: %w", ErrCorruptedManifest, err)
	}
	return valueCID, nil
}
