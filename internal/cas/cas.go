package cas

import (
	"context"

	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/storage"
)

type CAS struct {
	storage storage.ObjectStorage
	hasher  hasher.Hasher
}

type CID [32]byte

func getKey(cid CID) []byte {
	return append([]byte("chunk:"), cid[:]...)
}

func NewCAS(storage storage.ObjectStorage, hasher hasher.Hasher) *CAS {
	return &CAS{
		storage: storage,
		hasher:  hasher,
	}
}

func (c *CAS) Put(ctx context.Context, data []byte) (cid CID, err error) {
	cid = c.hasher.Hash(data)
	key := getKey(cid)
	exists, err := c.storage.Exists(ctx, key)
	if err != nil {
		return CID{}, err
	}

	if !exists {
		if err := c.storage.Put(ctx, key, data); err != nil {
			return CID{}, err
		}
	}

	return cid, nil
}

func (c *CAS) Get(ctx context.Context, cid CID) (data []byte, err error) {
	key := getKey(cid)
	return c.storage.Get(ctx, key)
}
