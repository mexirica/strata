package hasher

import (
	"crypto/sha256"
	"encoding/hex"
)

func NewSha256Hasher() Hasher {
	return &sha256Hasher{}
}

type sha256Hasher struct{}

func (h *sha256Hasher) Hash(data []byte) [32]byte {
	return sha256.Sum256(data)
}

func (h *sha256Hasher) ToString(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
