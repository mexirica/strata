package hasher

import (
	"crypto/sha256"
	"hash"

	"github.com/mexirica/strata/internal/cid"
)

func NewSha256Hasher() Hasher {
	return &sha256Hasher{}
}

type sha256Hasher struct{}

func (h *sha256Hasher) Algorithm() cid.Algorithm {
	return cid.AlgSHA256
}

func (h *sha256Hasher) Hash(data []byte) cid.CID {
	return newCID(cid.AlgSHA256, sha256.Sum256(data))
}

func (h *sha256Hasher) New() hash.Hash {
	return sha256.New()
}
