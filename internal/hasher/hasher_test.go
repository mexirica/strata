package hasher_test

import (
	"errors"
	"io"
	"testing"

	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/hasher"
)

func TestHashers(t *testing.T) {
	tests := []struct {
		name string
		h    hasher.Hasher
		alg  cid.Algorithm
	}{
		{name: "Blake3", h: hasher.NewBlake3Hasher(), alg: cid.AlgBlake3},
		{name: "Sha256", h: hasher.NewSha256Hasher(), alg: cid.AlgSHA256},
	}

	payload := []byte("hello strata cas storage")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.h.Algorithm() != tc.alg {
				t.Fatalf("expected algorithm %v, got %v", tc.alg, tc.h.Algorithm())
			}

			c1 := tc.h.Hash(payload)
			if c1.Algorithm() != tc.alg {
				t.Errorf("expected CID algorithm %v, got %v", tc.alg, c1.Algorithm())
			}

			// Test streaming via New() hash.Hash
			streamH := tc.h.New()
			_, err := io.WriteString(streamH, string(payload))
			if err != nil {
				t.Fatalf("write to hash failed: %v", err)
			}

			var streamDigest [32]byte
			copy(streamDigest[:], streamH.Sum(nil))
			c2, err := cid.NewCID(tc.alg, streamDigest)
			if err != nil {
				t.Fatalf("NewCID failed: %v", err)
			}

			if c1 != c2 {
				t.Errorf("Hash() result %v does not match New() streaming result %v", c1, c2)
			}
		})
	}
}

func TestForAlgorithm(t *testing.T) {
	for _, algorithm := range []cid.Algorithm{cid.AlgSHA256, cid.AlgBlake3} {
		h, err := hasher.ForAlgorithm(algorithm)
		if err != nil {
			t.Fatalf("ForAlgorithm(%v) failed: %v", algorithm, err)
		}
		if h.Algorithm() != algorithm {
			t.Fatalf("expected %v, got %v", algorithm, h.Algorithm())
		}
	}

	if _, err := hasher.ForAlgorithm(cid.Algorithm(0xff)); !errors.Is(err, hasher.ErrUnsupportedAlgorithm) {
		t.Fatalf("expected ErrUnsupportedAlgorithm, got %v", err)
	}
}
