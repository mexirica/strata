package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mexirica/strata/internal/storage"
	teststorage "github.com/mexirica/strata/internal/testkit/storage"
)

func TestFailureStore_PutFailsOnSecondCall(t *testing.T) {
    raw, err := storage.NewInMemoryBadger()
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = raw.Close() })

    store := teststorage.NewFailureStore(raw, teststorage.Rule{
        Operation: teststorage.OpPut,
        FailOn:    []int{2},
        Phase:     teststorage.Before,
    })

    ctx := context.Background()

    if err := store.Put(ctx, []byte("a"), []byte("1")); err != nil {
        t.Fatalf("first Put: %v", err)
    }

    err = store.Put(ctx, []byte("b"), []byte("2"))
    if !errors.Is(err, teststorage.ErrInjectedFailure) {
        t.Fatalf("expected injected failure, got %v", err)
    }

    var injected *teststorage.InjectedError
    if !errors.As(err, &injected) {
        t.Fatalf("expected InjectedError, got %T", err)
    }
    if injected.Call != 2 || injected.Phase != teststorage.Before {
        t.Fatalf("unexpected failure: %+v", injected)
    }
}

func TestFailureStore_ReopenPreservesWriteBeforeAfterFailure(t *testing.T) {
    harness := teststorage.NewHarness(
        filepath.Join(t.TempDir(), "badger"),
        teststorage.Rule{
            Operation: teststorage.OpPut,
            FailOn:    []int{1},
            Phase:     teststorage.After,
            Once:      true,
        },
    )
    t.Cleanup(func() { _ = harness.Close() })

    store, err := harness.Open()
    if err != nil {
        t.Fatal(err)
    }

    ctx := context.Background()
    err = store.Put(ctx, []byte("key"), []byte("value"))
    if !errors.Is(err, teststorage.ErrInjectedFailure) {
        t.Fatalf("expected injected failure, got %v", err)
    }

    store, err = harness.Reopen()
    if err != nil {
        t.Fatal(err)
    }

    value, err := store.Get(ctx, []byte("key"))
    if err != nil {
        t.Fatal(err)
    }
    if string(value) != "value" {
        t.Fatalf("got %q", value)
    }
}
