package hasher

import (
	"encoding/hex"

	"github.com/zeebo/blake3"
)

func NewBlake3Hasher() Hasher {
	return &blake3Hasher{}
}

type blake3Hasher struct{}

func (h *blake3Hasher) Hash(data []byte) [32]byte {
	return blake3.Sum256(data)
}

func (h *blake3Hasher) ToString(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}
