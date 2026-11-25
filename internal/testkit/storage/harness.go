package storage

import (
    "errors"
    "fmt"

    objectstorage "github.com/mexirica/strata/internal/storage"
)

type Harness struct {
    path  string
    rules []Rule
    raw   *objectstorage.Badger
    store *FailureStore
}

func NewHarness(path string, rules ...Rule) *Harness {
    return &Harness{
        path:  path,
        rules: append([]Rule(nil), rules...),
    }
}

func (h *Harness) Open() (*FailureStore, error) {
    if h.raw != nil {
        return nil, errors.New("storage is already open")
    }

    raw, err := objectstorage.NewBadger(h.path)
    if err != nil {
        return nil, fmt.Errorf("open test storage: %w", err)
    }

    h.raw = raw
    h.store = NewFailureStore(raw, h.rules...)
    return h.store, nil
}

func (h *Harness) Reopen() (*FailureStore, error) {
    if err := h.Close(); err != nil {
        return nil, err
    }
    return h.Open()
}

func (h *Harness) Raw() *objectstorage.Badger {
    return h.raw
}

func (h *Harness) Store() *FailureStore {
    return h.store
}

func (h *Harness) Close() error {
    if h.raw == nil {
        return nil
    }

    err := h.raw.Close()
    h.raw = nil
    h.store = nil

    if errors.Is(err, objectstorage.ErrClosed) {
        return nil
    }
    return err
}