package storage

import (
	"bytes"
	"context"
	"errors"

	"github.com/dgraph-io/badger/v4"
)

type Badger struct {
	db *badger.DB
}

func NewBadger(path string) (*Badger, error) {
	opts := badger.DefaultOptions(path).WithSyncWrites(true)
	opts.Logger = nil // suppress verbose logs during normal ops
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	return &Badger{
		db: db,
	}, nil
}

func NewInMemoryBadger() (*Badger, error) {
	opts := badger.DefaultOptions("").WithInMemory(true)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	return &Badger{
		db: db,
	}, nil
}

func (b *Badger) Put(ctx context.Context, key, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
	return translateError(err)
}

func (b *Badger) PutIfNotExists(ctx context.Context, key, value []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var inserted bool
		err := b.db.Update(func(txn *badger.Txn) error {
			item, err := txn.Get(key)
			if err == nil {
				inserted = false
				return item.Value(func(existing []byte) error {
					if !bytes.Equal(existing, value) {
						return ErrContentMismatch
					}
					return nil
				})
			}
			if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			inserted = true
			return txn.Set(key, value)
		})
		if errors.Is(err, badger.ErrConflict) {
			// Optimistic concurrency conflict: retry so the next attempt reads the newly committed key
			continue
		}
		return inserted, translateError(err)
	}
}

func (b *Badger) Get(ctx context.Context, key []byte) ([]byte, error) {
	var data []byte
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			data = append([]byte(nil), val...)
			return nil
		})
	})
	return data, translateError(err)
}

func (b *Badger) Exists(ctx context.Context, key []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	err := b.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(key)
		return err
	})
	if err == badger.ErrKeyNotFound {
		return false, nil
	}
	return err == nil, translateError(err)
}

func (b *Badger) Delete(ctx context.Context, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key)
	})
	return translateError(err)
}

func (b *Badger) ListPage(ctx context.Context, prefix, after []byte, limit int) ([]Object, []byte, error) {
	if limit <= 0 {
		return nil, nil, errors.New("list limit must be greater than zero")
	}
	var objects []Object
	var next []byte
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	err := b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()
		seek := prefix
		if len(after) > 0 {
			seek = after
		}
		for it.Seek(seek); it.ValidForPrefix(prefix); it.Next() {
			if len(after) > 0 && bytes.Equal(it.Item().Key(), after) {
				continue
			}
			if len(objects) == limit {
				next = append([]byte(nil), objects[len(objects)-1].Key...)
				break
			}
			item := it.Item()
			key := append([]byte(nil), item.Key()...)
			var value []byte
			if err := item.Value(func(data []byte) error {
				value = append([]byte(nil), data...)
				return nil
			}); err != nil {
				return err
			}
			objects = append(objects, Object{Key: key, Value: value})
		}
		return nil
	})
	return objects, next, translateError(err)
}

func (b *Badger) Close() error {
	return translateError(b.db.Close())
}

func translateError(err error) error {
	switch {
	case errors.Is(err, badger.ErrKeyNotFound):
		return ErrNotFound
	case errors.Is(err, badger.ErrDBClosed):
		return ErrClosed
	default:
		return err
	}
}
