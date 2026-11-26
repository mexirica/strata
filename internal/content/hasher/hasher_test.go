package hasher_test

import (
	"errors"
	"io"
	"testing"

	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/content/hasher"
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
	tests := []struct {
		name      string
		algorithm cid.Algorithm
		wantErr   error
	}{
		{name: "SHA-256", algorithm: cid.AlgSHA256},
		{name: "BLAKE3", algorithm: cid.AlgBlake3},
		{name: "unsupported", algorithm: cid.Algorithm(0xff), wantErr: hasher.ErrUnsupportedAlgorithm},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := hasher.ForAlgorithm(tc.algorithm)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ForAlgorithm returned %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if h.Algorithm() != tc.algorithm {
				t.Fatalf("expected %v, got %v", tc.algorithm, h.Algorithm())
			}
		})
	}
}
