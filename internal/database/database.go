package database

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// DB owns one encrypted Badger database. Writers are serialized so callers can
// make read/check/write decisions without retries or partially replayed callbacks.
type DB struct {
	engine    *badger.DB
	writeMu   sync.Mutex
	lifeMu    sync.RWMutex
	authKey   []byte
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}
type Tx struct {
	tx      *badger.Txn
	err     error
	cursors []*Cursor
}
type Bucket struct {
	tx     *Tx
	name   []byte
	prefix []byte
}
type BucketStats struct{ KeyN int }
type Cursor struct {
	bucket   *Bucket
	iterator *badger.Iterator
	reverse  bool
	key      []byte
}

func Open(path string, key []byte) (*DB, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("database encryption requires a 32-byte key")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return nil, err
	}
	opts := badger.DefaultOptions(path).WithEncryptionKey(bytes.Clone(key)).WithSyncWrites(true).WithLogger(nil).
		WithIndexCacheSize(8 << 20).WithBlockCacheSize(8 << 20).WithMemTableSize(32 << 20).WithNumMemtables(2)
	engine, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open encrypted database: %w", err)
	}
	authKey, err := hkdf.Key(sha256.New, key, nil, "mcpproxy client token authentication", 32)
	if err != nil {
		engine.Close()
		return nil, err
	}
	db := &DB{engine: engine, authKey: authKey, stop: make(chan struct{}), done: make(chan struct{})}
	go db.collectValueLog()
	return db, nil
}

// Reclaim obsolete value-log files left by token rotations and retention deletes.
func (db *DB) collectValueLog() {
	defer close(db.done)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-db.stop:
			return
		case <-ticker.C:
			for {
				select {
				case <-db.stop:
					return
				default:
				}
				if err := db.engine.RunValueLogGC(0.5); err != nil {
					break
				}
			}
		}
	}
}
func (db *DB) Close() error {
	db.closeOnce.Do(func() {
		close(db.stop)
		<-db.done
		db.lifeMu.Lock()
		defer db.lifeMu.Unlock()
		db.closeErr = db.engine.Close()
	})
	return db.closeErr
}
func (db *DB) View(fn func(*Tx) error) error {
	db.lifeMu.RLock()
	defer db.lifeMu.RUnlock()
	return db.engine.View(func(txn *badger.Txn) error {
		tx := &Tx{tx: txn}
		defer tx.closeCursors()
		if err := fn(tx); err != nil {
			return err
		}
		return tx.err
	})
}
func (db *DB) Update(fn func(*Tx) error) error {
	db.lifeMu.RLock()
	defer db.lifeMu.RUnlock()
	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	return db.engine.Update(func(txn *badger.Txn) error {
		tx := &Tx{tx: txn}
		defer tx.closeCursors()
		if err := fn(tx); err != nil {
			return err
		}
		return tx.err
	})
}

var ErrBucketNotFound = errors.New("bucket not found")

func key(kind byte, name []byte) []byte { return append([]byte{kind}, name...) }
func rows(name []byte) []byte {
	p := make([]byte, 5+len(name))
	p[0] = 'R'
	binary.BigEndian.PutUint32(p[1:], uint32(len(name)))
	copy(p[5:], name)
	return p
}
func (tx *Tx) read(k []byte) []byte {
	item, err := tx.tx.Get(k)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		tx.err = err
		return nil
	}
	value, err := item.ValueCopy(nil)
	if err != nil {
		tx.err = err
		return nil
	}
	if value == nil {
		return []byte{}
	}
	return value
}
func (tx *Tx) Bucket(name []byte) *Bucket {
	if tx.read(key('B', name)) == nil {
		return nil
	}
	return &Bucket{tx: tx, name: bytes.Clone(name), prefix: rows(name)}
}
func (tx *Tx) CreateBucketIfNotExists(name []byte) (*Bucket, error) {
	if len(name) == 0 {
		return nil, fmt.Errorf("bucket name required")
	}
	if existing := tx.Bucket(name); existing != nil {
		return existing, nil
	}
	if tx.err != nil {
		return nil, tx.err
	}
	if err := tx.tx.Set(key('B', name), []byte{1}); err != nil {
		return nil, err
	}
	return &Bucket{tx: tx, name: bytes.Clone(name), prefix: rows(name)}, nil
}
func (tx *Tx) DeleteBucket(name []byte) error {
	b := tx.Bucket(name)
	if b == nil {
		return tx.err
	}
	if err := b.ForEach(func(k, v []byte) error { return b.Delete(k) }); err != nil {
		return err
	}
	if err := tx.tx.Delete(key('S', name)); err != nil {
		return err
	}
	return tx.tx.Delete(key('B', name))
}
func (tx *Tx) ForEach(fn func([]byte, *Bucket) error) error {
	it := tx.tx.NewIterator(badger.IteratorOptions{Prefix: []byte{'B'}, PrefetchValues: false})
	defer it.Close()
	var names [][]byte
	for it.Rewind(); it.Valid(); it.Next() {
		names = append(names, bytes.Clone(it.Item().Key()[1:]))
	}
	for _, name := range names {
		if err := fn(name, tx.Bucket(name)); err != nil {
			return err
		}
	}
	return tx.err
}
func (b *Bucket) row(k []byte) []byte {
	out := make([]byte, 0, len(b.prefix)+len(k))
	out = append(out, b.prefix...)
	return append(out, k...)
}
func (b *Bucket) Get(k []byte) []byte { return b.tx.read(b.row(k)) }
func (b *Bucket) Put(k, v []byte) error {
	if len(k) == 0 {
		return fmt.Errorf("record key required")
	}
	return b.tx.tx.Set(b.row(k), bytes.Clone(v))
}
func (b *Bucket) Delete(k []byte) error { return b.tx.tx.Delete(b.row(k)) }
func (b *Bucket) ForEach(fn func([]byte, []byte) error) error {
	cursor := b.Cursor()
	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return b.tx.err
}
func (b *Bucket) NextSequence() (uint64, error) {
	raw := b.tx.read(key('S', b.name))
	if b.tx.err != nil {
		return 0, b.tx.err
	}
	var n uint64
	if raw != nil {
		if len(raw) != 8 {
			return 0, fmt.Errorf("invalid bucket sequence")
		}
		n = binary.BigEndian.Uint64(raw)
	}
	if n == ^uint64(0) {
		return 0, fmt.Errorf("bucket sequence exhausted")
	}
	n++
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, n)
	return n, b.tx.tx.Set(key('S', b.name), value)
}
func (b *Bucket) Stats() BucketStats {
	it := b.tx.tx.NewIterator(badger.IteratorOptions{Prefix: b.prefix, PrefetchValues: false})
	defer it.Close()
	var count int
	for it.Rewind(); it.Valid(); it.Next() {
		count++
	}
	return BucketStats{KeyN: count}
}
func (b *Bucket) Cursor() *Cursor {
	c := &Cursor{bucket: b}
	b.tx.cursors = append(b.tx.cursors, c)
	return c
}
func (tx *Tx) closeCursors() {
	for _, c := range tx.cursors {
		if c.iterator != nil {
			c.iterator.Close()
			c.iterator = nil
		}
	}
}
func (c *Cursor) open(reverse bool) {
	if c.iterator != nil {
		c.iterator.Close()
	}
	// Reverse seeks start at the prefix's upper bound, which lies outside the
	// bucket. Enforce the prefix below, after positioning, rather than in Seek.
	options := badger.IteratorOptions{Reverse: reverse, PrefetchValues: false}
	if !reverse {
		options.Prefix = c.bucket.prefix
	}
	c.iterator = c.bucket.tx.tx.NewIterator(options)
	c.reverse = reverse
}
func (c *Cursor) current() ([]byte, []byte) {
	for c.iterator.ValidForPrefix(c.bucket.prefix) {
		full := c.iterator.Item().KeyCopy(nil)
		k := full[len(c.bucket.prefix):]
		v := c.bucket.Get(k)
		if v != nil {
			c.key = full
			return k, v
		}
		c.iterator.Next()
	}
	c.key = nil
	return nil, nil
}
func (c *Cursor) First() ([]byte, []byte) {
	c.open(false)
	c.iterator.Seek(c.bucket.prefix)
	return c.current()
}
func (c *Cursor) Last() ([]byte, []byte) {
	c.open(true)
	end := bytes.Clone(c.bucket.prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 255 {
			end[i]++
			end = end[:i+1]
			break
		}
	}
	c.iterator.Seek(end)
	if c.iterator.Valid() && bytes.Equal(c.iterator.Item().Key(), end) {
		c.iterator.Next()
	}
	return c.current()
}
func (c *Cursor) step(reverse bool) ([]byte, []byte) {
	if c.iterator == nil || c.key == nil {
		return nil, nil
	}
	if c.reverse != reverse {
		oldKey := bytes.Clone(c.key)
		c.open(reverse)
		c.iterator.Seek(oldKey)
		if c.iterator.Valid() && bytes.Equal(c.iterator.Item().Key(), oldKey) {
			c.iterator.Next()
		}
	} else {
		c.iterator.Next()
	}
	return c.current()
}
func (c *Cursor) Next() ([]byte, []byte) { return c.step(false) }
func (c *Cursor) Prev() ([]byte, []byte) { return c.step(true) }
func (c *Cursor) Seek(k []byte) ([]byte, []byte) {
	c.open(false)
	c.iterator.Seek(c.bucket.row(k))
	return c.current()
}
func (c *Cursor) Delete() error {
	if c.key == nil {
		return nil
	}
	return c.bucket.tx.tx.Delete(c.key)
}

func (db *DB) AuthenticationKey() []byte { return bytes.Clone(db.authKey) }
