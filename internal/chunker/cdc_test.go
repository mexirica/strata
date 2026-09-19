package chunker_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/mexirica/strata/internal/chunker"
)

func TestCDC_Split_Reconstruction(t *testing.T) {
	algs := []chunker.CdcAlgorithm{
		chunker.FastCDC,
		chunker.UltraCDC,
		chunker.JC,
	}

	payload := make([]byte, 128*1024)
	_, _ = rand.Read(payload)

	for _, alg := range algs {
		t.Run(string(alg), func(t *testing.T) {
			cfg := chunker.Config{
				Algorithm:  alg,
				MinSize:    4 * 1024,
				NormalSize: 16 * 1024,
				MaxSize:    64 * 1024,
			}

			cdc, err := chunker.NewCDC(cfg)
			if err != nil {
				t.Fatalf("failed to create cdc: %v", err)
			}

			var reassembled bytes.Buffer
			chunkCount := 0

			err = cdc.Split(context.Background(), bytes.NewReader(payload), func(chunk []byte) error {
				chunkCount++
				reassembled.Write(chunk)
				return nil
			})
			if err != nil {
				t.Fatalf("split failed: %v", err)
			}

			if chunkCount == 0 {
				t.Fatal("expected chunks, got 0")
			}

			if !bytes.Equal(payload, reassembled.Bytes()) {
				t.Fatalf("reassembled content does not match original for %s", alg)
			}
		})
	}
}

func TestCDC_DefaultConfig(t *testing.T) {
	config := chunker.DefaultConfig()
	if config.MinSize != 256*1024 || config.NormalSize != 1024*1024 || config.MaxSize != 4*1024*1024 {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if _, err := chunker.NewCDC(config); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
}

func TestCDC_RejectsUnsafeConfig(t *testing.T) {
	tests := []chunker.Config{
		{Algorithm: chunker.FastCDC, MinSize: 0, NormalSize: 1024, MaxSize: 4096},
		{Algorithm: chunker.FastCDC, MinSize: 64, NormalSize: 1000, MaxSize: 4096},
		{Algorithm: chunker.FastCDC, MinSize: 64, NormalSize: 1024, MaxSize: chunker.MaxSupportedChunkSize + 1},
		{Algorithm: chunker.FastCDC, MinSize: 1024, NormalSize: 1024, MaxSize: 4096},
	}
	for _, config := range tests {
		if _, err := chunker.NewCDC(config); !errors.Is(err, chunker.ErrInvalidConfig) {
			t.Fatalf("expected ErrInvalidConfig for %+v, got %v", config, err)
		}
	}
}

func TestCDC_FastCDCDeterministicVector(t *testing.T) {
	cdc, err := chunker.NewCDC(chunker.DefaultConfig())
	if err != nil {
		t.Fatalf("NewCDC failed: %v", err)
	}
	payload := make([]byte, 8*1024*1024)
	var state uint32 = 1
	for i := range payload {
		state = state*1664525 + 1013904223
		payload[i] = byte(state >> 24)
	}

	var lengths []int
	err = cdc.Split(context.Background(), bytes.NewReader(payload), func(chunk []byte) error {
		lengths = append(lengths, len(chunk))
		return nil
	})
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}
	want := []int{1180088, 1165599, 478739, 1085537, 963206, 1488499, 1083334, 943606}
	if !reflect.DeepEqual(lengths, want) {
		t.Fatalf("FastCDC vector changed: got %v, want %v", lengths, want)
	}
}

func TestCDC_CancellationBeforeCallback(t *testing.T) {
	cdc, err := chunker.NewCDC(chunker.Config{
		Algorithm:  chunker.FastCDC,
		MinSize:    64,
		NormalSize: 128,
		MaxSize:    256,
	})
	if err != nil {
		t.Fatalf("NewCDC failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{reader: bytes.NewReader(make([]byte, 1024)), cancel: cancel}
	called := false
	err = cdc.Split(ctx, reader, func([]byte) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if called {
		t.Fatal("callback called after context cancellation")
	}
}

type cancelingReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
}
