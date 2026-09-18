package manifeststore

import (
	"context"
	"encoding/json"
	"uuid"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/storage"
)

type ManifestStore struct {
	storage *storage.Badger
}

type FileManifest struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Chunks []cas.CID `json:"chunks"`
}

func getKey(id uuid.UUID) []byte {
	return append([]byte("manifest:"), id[:]...)
}

func NewManifestStore(storage *storage.Badger) *ManifestStore {
	return &ManifestStore{
		storage: storage,
	}
}

func (s *ManifestStore) Put(
	ctx context.Context,
	manifest FileManifest,
) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	return s.storage.Put(ctx, getKey(manifest.ID), data)
}

func (s *ManifestStore) Get(
	ctx context.Context,
	id uuid.UUID,
) (FileManifest, error) {
	data, err := s.storage.Get(ctx, getKey(id))
	if err != nil {
		return FileManifest{}, err
	}

	var manifest FileManifest

	if err := json.Unmarshal(data, &manifest); err != nil {
		return FileManifest{}, err
	}

	return manifest, nil
}

func (s *ManifestStore) Delete(
	ctx context.Context,
	id uuid.UUID,
) error {
	return s.storage.Delete(ctx, getKey(id))
}
