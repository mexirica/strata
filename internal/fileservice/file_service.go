package fileservice

import (
	"context"

	"io"
	"uuid"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/manifeststore"
)

type FileService struct {
	chunker       *chunker.CDC
	cas           *cas.CAS
	manifestStore *manifeststore.ManifestStore
}

func NewFileService(chunker *chunker.CDC, cas *cas.CAS, manifestStore *manifeststore.ManifestStore) *FileService {
	return &FileService{
		chunker:       chunker,
		cas:           cas,
		manifestStore: manifestStore,
	}
}

func (s *FileService) Store(
	ctx context.Context,
	name string,
	r io.Reader,
) (uuid.UUID, error) {

	var (
		cids []cas.CID
		size int64
	)

	err := s.chunker.Split(ctx, r, func(chunk []byte) error {
		cid, err := s.cas.Put(ctx, chunk)
		if err != nil {
			return err
		}

		cids = append(cids, cid)
		size += int64(len(chunk))

		return nil
	})

	if err != nil {
		return uuid.UUID{}, err
	}

	manifestId := uuid.New()

	if err := s.manifestStore.Put(ctx, manifeststore.FileManifest{
		ID:     manifestId,
		Name:   name,
		Size:   size,
		Chunks: cids,
	}); err != nil {
		return uuid.UUID{}, err
	}
	return manifestId, nil
}
