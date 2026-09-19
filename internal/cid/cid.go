package cid

import (
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	ErrInvalidCID           = errors.New("invalid CID")
	ErrUnsupportedAlgorithm = errors.New("unsupported CID algorithm")
)

type Algorithm uint8

const (
	AlgSHA256 Algorithm = 0x12 // SHA2-256 (multihash code 0x12)
	AlgBlake3 Algorithm = 0x1e // BLAKE3 (multihash code 0x1e)
)

func (a Algorithm) String() string {
	switch a {
	case AlgSHA256:
		return "sha256"
	case AlgBlake3:
		return "blake3"
	default:
		return fmt.Sprintf("unknown(0x%x)", uint8(a))
	}
}

func (a Algorithm) IsValid() bool {
	return a == AlgSHA256 || a == AlgBlake3
}

// CID represents a self-describing Content Identifier carrying the hash algorithm and digest.
type CID struct {
	alg    Algorithm
	digest [32]byte
}

func NewCID(alg Algorithm, digest [32]byte) (CID, error) {
	if !alg.IsValid() {
		return CID{}, fmt.Errorf("%w: 0x%x", ErrUnsupportedAlgorithm, uint8(alg))
	}
	return CID{alg: alg, digest: digest}, nil
}

func (c CID) Algorithm() Algorithm {
	return c.alg
}

func (c CID) Digest() [32]byte {
	return c.digest
}

func (c CID) IsValid() bool {
	return c.alg.IsValid()
}

// Bytes returns the multihash-style binary representation:
// [1 byte algorithm code][1 byte digest length (32)][32 bytes digest]
func (c CID) Bytes() []byte {
	if !c.IsValid() {
		return nil
	}
	buf := make([]byte, 34)
	buf[0] = byte(c.alg)
	buf[1] = 32
	copy(buf[2:], c.digest[:])
	return buf
}

// String returns the hex-encoded string representation of the CID.
func (c CID) String() string {
	if !c.IsValid() {
		return ""
	}
	return hex.EncodeToString(c.Bytes())
}

// ParseCID parses a hex-encoded string into a CID.
func ParseCID(s string) (CID, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return CID{}, fmt.Errorf("decode hex CID: %w", err)
	}
	return ParseCIDBytes(b)
}

// ParseCIDBytes parses binary multihash bytes into a CID.
func ParseCIDBytes(b []byte) (CID, error) {
	if len(b) != 34 {
		return CID{}, fmt.Errorf("invalid CID byte length: got %d, want 34", len(b))
	}

	alg := Algorithm(b[0])
	if !alg.IsValid() {
		return CID{}, fmt.Errorf("unsupported algorithm code: 0x%x", b[0])
	}

	length := int(b[1])
	if length != 32 {
		return CID{}, fmt.Errorf("invalid digest length: got %d, want 32", length)
	}

	var digest [32]byte
	copy(digest[:], b[2:34])

	return NewCID(alg, digest)
}

// MarshalText implements encoding.TextMarshaler for JSON serialization.
func (c CID) MarshalText() ([]byte, error) {
	if !c.IsValid() {
		return nil, ErrInvalidCID
	}
	return []byte(c.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler for JSON deserialization.
func (c *CID) UnmarshalText(text []byte) error {
	parsed, err := ParseCID(string(text))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}
