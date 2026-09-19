package cid_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mexirica/strata/internal/cid"
)

func TestCID_Roundtrip(t *testing.T) {
	var digest [32]byte
	for i := range digest {
		digest[i] = byte(i)
	}

	c, err := cid.NewCID(cid.AlgBlake3, digest)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}
	s := c.String()

	parsed, err := cid.ParseCID(s)
	if err != nil {
		t.Fatalf("ParseCID failed: %v", err)
	}

	if parsed != c {
		t.Fatalf("expected parsed CID to equal original: got %v, want %v", parsed, c)
	}

	if parsed.Algorithm() != cid.AlgBlake3 {
		t.Errorf("expected algorithm %v, got %v", cid.AlgBlake3, parsed.Algorithm())
	}
	if parsed.Digest() != digest {
		t.Errorf("expected digest %v, got %v", digest, parsed.Digest())
	}
}

func TestCID_JSONSerialization(t *testing.T) {
	var digest [32]byte
	digest[0] = 0xab
	digest[31] = 0xcd

	c, err := cid.NewCID(cid.AlgSHA256, digest)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	expectedStr := `"` + c.String() + `"`
	if string(data) != expectedStr {
		t.Errorf("expected JSON %s, got %s", expectedStr, string(data))
	}

	var unmarshaled cid.CID
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if unmarshaled != c {
		t.Errorf("expected %v, got %v", c, unmarshaled)
	}
}

func TestCID_RejectsInvalidConstructionAndZeroValue(t *testing.T) {
	if _, err := cid.NewCID(cid.Algorithm(0xff), [32]byte{}); !errors.Is(err, cid.ErrUnsupportedAlgorithm) {
		t.Fatalf("expected ErrUnsupportedAlgorithm, got %v", err)
	}

	var zero cid.CID
	if zero.IsValid() || zero.String() != "" || zero.Bytes() != nil {
		t.Fatal("zero CID must remain invalid and have no representation")
	}
	if _, err := zero.MarshalText(); !errors.Is(err, cid.ErrInvalidCID) {
		t.Fatalf("expected ErrInvalidCID, got %v", err)
	}
}

func TestCID_Invalid(t *testing.T) {
	cases := []string{
		"",
		"invalid-hex",
		"1e20", // too short
	}

	for _, tc := range cases {
		if _, err := cid.ParseCID(tc); err == nil {
			t.Errorf("expected error for input %q, got nil", tc)
		}
	}
}

func FuzzParseCIDBytes(f *testing.F) {
	valid := make([]byte, 34)
	valid[0] = byte(cid.AlgBlake3)
	valid[1] = 32
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := cid.ParseCIDBytes(data)
		if err != nil {
			return
		}
		if !parsed.IsValid() {
			t.Fatal("successfully parsed CID must be valid")
		}
		reparsed, err := cid.ParseCIDBytes(parsed.Bytes())
		if err != nil || reparsed != parsed {
			t.Fatalf("binary roundtrip failed: parsed=%v reparsed=%v err=%v", parsed, reparsed, err)
		}
	})
}
