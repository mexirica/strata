package fileservice

import (
	"context"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/chunker"
	"io"
	"uuid"
)

type FileService struct {
	chunker *chunker.CDC
	cas     *cas.CAS
}

type FileManifest struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Chunks []cas.CID `json:"chunks"`
}

func NewFileService(chunker *chunker.CDC, cas *cas.CAS) *FileService {
	return &FileService{
		chunker: chunker,
		cas:     cas,
	}
}

func (s *FileService) Store(
	ctx context.Context,
	name string,
	r io.Reader,
) (FileManifest, error) {

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
		return FileManifest{}, err
	}

	return FileManifest{
		ID:     uuid.New(),
		Name:   name,
		Size:   size,
		Chunks: cids,
	}, nil
}
