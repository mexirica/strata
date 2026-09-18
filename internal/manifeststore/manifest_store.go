package manifeststore

import (
	"context"
	"encoding/json"
	"uuid"

	"github.com/mexirica/strata/internal/fileservice"
	"github.com/mexirica/strata/internal/storage"
)

type ManifestStore struct {
	storage *storage.Badger
}

func NewManifestStore(storage *storage.Badger) *ManifestStore {
	return &ManifestStore{
		storage: storage,
	}
}

func (s *ManifestStore) Put(
	ctx context.Context,
	manifest fileservice.FileManifest,
) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	return s.storage.Put(ctx, manifest.ID[:], data)
}

func (s *ManifestStore) Get(
	ctx context.Context,
	id uuid.UUID,
) (fileservice.FileManifest, error) {
	data, err := s.storage.Get(ctx, id[:])
	if err != nil {
		return fileservice.FileManifest{}, err
	}

	var manifest fileservice.FileManifest

	if err := json.Unmarshal(data, &manifest); err != nil {
		return fileservice.FileManifest{}, err
	}

	return manifest, nil
}

func (s *ManifestStore) Delete(
	ctx context.Context,
	id uuid.UUID,
) error {
	return s.storage.Delete(ctx, id[:])
}
