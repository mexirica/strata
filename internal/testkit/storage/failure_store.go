// Package storage contains test storage implementations used by the project.
package storage

import (
    "context"
    "errors"
    "fmt"
    "sync"

    objectstorage "github.com/mexirica/strata/internal/storage"
)

type Operation string

const (
    OpPut            Operation = "Put"
    OpPutIfNotExists Operation = "PutIfNotExists"
    OpGet            Operation = "Get"
    OpExists         Operation = "Exists"
    OpDelete         Operation = "Delete"
    OpListPage       Operation = "ListPage"
    OpListKeysPage   Operation = "ListKeysPage"
    OpClose          Operation = "Close"
)

type Phase string

const (
    Before Phase = "before"
    After  Phase = "after"
)

var ErrInjectedFailure = errors.New("injected object storage failure")

type InjectedError struct {
    Operation Operation
    Call      int
    Phase     Phase
}

func (e *InjectedError) Error() string {
    return fmt.Sprintf(
        "%s: operation=%s call=%d phase=%s",
        ErrInjectedFailure,
        e.Operation,
        e.Call,
        e.Phase,
    )
}

func (e *InjectedError) Unwrap() error {
    return ErrInjectedFailure
}

type Rule struct {
    Operation Operation
    FailOn    []int
    Every     int
    Phase     Phase
    Once      bool
}

type FailureStore struct {
    inner objectstorage.ObjectStorage
    rules []Rule

    mu     sync.Mutex
    counts map[Operation]int
    fired  []bool
}

var _ objectstorage.ObjectStorage = (*FailureStore)(nil)

func NewFailureStore(
    inner objectstorage.ObjectStorage,
    rules ...Rule,
) *FailureStore {
    copied := append([]Rule(nil), rules...)

    return &FailureStore{
        inner:  inner,
        rules:  copied,
        counts: make(map[Operation]int),
        fired:  make([]bool, len(copied)),
    }
}

func (s *FailureStore) nextCall(op Operation) int {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.counts[op]++
    return s.counts[op]
}

func (s *FailureStore) injected(
    op Operation,
    call int,
    phase Phase,
) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    for i, rule := range s.rules {
        if rule.Operation != op || rule.Phase != phase {
            continue
        }
        if rule.Once && s.fired[i] {
            continue
        }

        matches := false
        for _, wanted := range rule.FailOn {
            if call == wanted {
                matches = true
                break
            }
        }
        if rule.Every > 0 && call%rule.Every == 0 {
            matches = true
        }
        if !matches {
            continue
        }

        s.fired[i] = true
        return &InjectedError{
            Operation: op,
            Call:      call,
            Phase:     phase,
        }
    }

    return nil
}

func (s *FailureStore) before(op Operation) (int, error) {
    call := s.nextCall(op)
    return call, s.injected(op, call, Before)
}

func (s *FailureStore) after(
    op Operation,
    call int,
    innerErr error,
) error {
    if innerErr != nil {
        return innerErr
    }
    return s.injected(op, call, After)
}

func (s *FailureStore) Calls(op Operation) int {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.counts[op]
}

func (s *FailureStore) Put(
    ctx context.Context,
    key, value []byte,
) error {
    call, err := s.before(OpPut)
    if err != nil {
        return err
    }

    err = s.inner.Put(ctx, key, value)
    return s.after(OpPut, call, err)
}

func (s *FailureStore) PutIfNotExists(
    ctx context.Context,
    key, value []byte,
) (bool, error) {
    call, err := s.before(OpPutIfNotExists)
    if err != nil {
        return false, err
    }

    inserted, err := s.inner.PutIfNotExists(ctx, key, value)
    if err = s.after(OpPutIfNotExists, call, err); err != nil {
        return false, err
    }
    return inserted, nil
}

func (s *FailureStore) Get(
    ctx context.Context,
    key []byte,
) ([]byte, error) {
    call, err := s.before(OpGet)
    if err != nil {
        return nil, err
    }

    value, err := s.inner.Get(ctx, key)
    if err = s.after(OpGet, call, err); err != nil {
        return nil, err
    }
    return value, nil
}

func (s *FailureStore) Exists(
    ctx context.Context,
    key []byte,
) (bool, error) {
    call, err := s.before(OpExists)
    if err != nil {
        return false, err
    }

    exists, err := s.inner.Exists(ctx, key)
    if err = s.after(OpExists, call, err); err != nil {
        return false, err
    }
    return exists, nil
}

func (s *FailureStore) Delete(
    ctx context.Context,
    key []byte,
) error {
    call, err := s.before(OpDelete)
    if err != nil {
        return err
    }

    err = s.inner.Delete(ctx, key)
    return s.after(OpDelete, call, err)
}

func (s *FailureStore) ListPage(
    ctx context.Context,
    prefix, after []byte,
    limit int,
) ([]objectstorage.Object, []byte, error) {
    call, err := s.before(OpListPage)
    if err != nil {
        return nil, nil, err
    }

    objects, next, err := s.inner.ListPage(ctx, prefix, after, limit)
    if err = s.after(OpListPage, call, err); err != nil {
        return nil, nil, err
    }
    return objects, next, nil
}

func (s *FailureStore) ListKeysPage(
    ctx context.Context,
    prefix, after []byte,
    limit int,
) ([][]byte, []byte, error) {
    call, err := s.before(OpListKeysPage)
    if err != nil {
        return nil, nil, err
    }

    keys, next, err := s.inner.ListKeysPage(ctx, prefix, after, limit)
    if err = s.after(OpListKeysPage, call, err); err != nil {
        return nil, nil, err
    }
    return keys, next, nil
}

func (s *FailureStore) Close() error {
    call, err := s.before(OpClose)
    if err != nil {
        return err
    }

    err = s.inner.Close()
    return s.after(OpClose, call, err)
}