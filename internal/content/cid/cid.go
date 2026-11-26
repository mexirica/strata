// Package cid encodes and parses self-describing content identifiers.
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
	AlgSHA256      Algorithm = 0x12 // SHA2-256 (multihash code 0x12)
	AlgBlake3      Algorithm = 0x1e // BLAKE3 (multihash code 0x1e)
	digestSize               = 32
	cidHeaderSize            = 2
	encodedCIDSize           = cidHeaderSize + digestSize
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
	digest [digestSize]byte
}

func NewCID(alg Algorithm, digest [digestSize]byte) (CID, error) {
	if !alg.IsValid() {
		return CID{}, fmt.Errorf("%w: 0x%x", ErrUnsupportedAlgorithm, uint8(alg))
	}
	return CID{alg: alg, digest: digest}, nil
}

func (c CID) Algorithm() Algorithm {
	return c.alg
}

func (c CID) Digest() [digestSize]byte {
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
	buf := make([]byte, encodedCIDSize)
	buf[0] = byte(c.alg)
	buf[1] = digestSize
	copy(buf[cidHeaderSize:], c.digest[:])
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
	if len(b) != encodedCIDSize {
		return CID{}, fmt.Errorf("invalid CID byte length: got %d, want %d", len(b), encodedCIDSize)
	}

	alg := Algorithm(b[0])
	if !alg.IsValid() {
		return CID{}, fmt.Errorf("unsupported algorithm code: 0x%x", b[0])
	}

	length := int(b[1])
	if length != digestSize {
		return CID{}, fmt.Errorf("invalid digest length: got %d, want %d", length, digestSize)
	}

	var digest [digestSize]byte
	copy(digest[:], b[cidHeaderSize:encodedCIDSize])

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
