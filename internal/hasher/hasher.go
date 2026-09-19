package hasher

import (
	"errors"
	"fmt"
	"hash"

	"github.com/mexirica/strata/internal/cid"
)

var ErrUnsupportedAlgorithm = errors.New("unsupported hash algorithm")

type Hasher interface {
	Algorithm() cid.Algorithm
	Hash(data []byte) cid.CID
	New() hash.Hash
}

func ForAlgorithm(algorithm cid.Algorithm) (Hasher, error) {
	switch algorithm {
	case cid.AlgSHA256:
		return NewSha256Hasher(), nil
	case cid.AlgBlake3:
		return NewBlake3Hasher(), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, algorithm)
	}
}

func newCID(algorithm cid.Algorithm, digest [32]byte) cid.CID {
	value, err := cid.NewCID(algorithm, digest)
	if err != nil {
		panic(err)
	}
	return value
}
