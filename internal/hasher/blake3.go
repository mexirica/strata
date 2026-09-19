package hasher

import (
	"hash"

	"github.com/mexirica/strata/internal/cid"
	"github.com/zeebo/blake3"
)

func NewBlake3Hasher() Hasher {
	return &blake3Hasher{}
}

type blake3Hasher struct{}

func (h *blake3Hasher) Algorithm() cid.Algorithm {
	return cid.AlgBlake3
}

func (h *blake3Hasher) Hash(data []byte) cid.CID {
	return newCID(cid.AlgBlake3, blake3.Sum256(data))
}

func (h *blake3Hasher) New() hash.Hash {
	return blake3.New()
}
