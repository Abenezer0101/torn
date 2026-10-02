// Package torn implements an append-only key-value log and the durability
// property it owes its callers:
//
//	After a crash, recovery returns a prefix of the records that were
//	written, reaching at least as far as the last commit.
//
// Lost writes, resurrected writes and silent corruption are all violations of
// that one sentence. The package exists to try to violate it on purpose: every
// byte reaches storage through [Storage], so a seeded PRNG can do what real
// hardware does on power loss. [SimDisk] is that fake; [FileDisk] is a real
// file. Both are held to the same property by the same tests.
package torn

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// Storage is the single point through which every byte reaches the platter.
// Narrow on purpose: a wider interface would give the store somewhere to hide
// durability assumptions the simulator cannot reach.
type Storage interface {
	// Write queues bytes at off. They are not durable until Sync returns.
	Write(off int, b []byte) error
	// Sync makes every prior Write durable.
	Sync() error
	// ReadAll returns everything currently durable.
	ReadAll() ([]byte, error)
}

// HeaderSize is crc32 | keyLen | valLen, all little-endian.
const HeaderSize = 12

// MaxRecord caps a single record. Without it a corrupt length field asks for a
// multi-gigabyte allocation, and a recovery that OOMs is still a recovery that
// failed.
const MaxRecord = 1 << 24 // 16 MiB

// ErrNotARecord means the bytes at this position do not begin a valid record.
// Recovery treats it as the end of the log, which is the whole point: a
// torn tail must stop replay rather than be guessed at.
var ErrNotARecord = errors.New("torn: not a record")

// Encode lays out one record. With guards, the leading four bytes are a CRC32
// of everything after them; without, they are left zero and nothing in the
// record can detect damage to it.
func Encode(key, val string, guards bool) []byte {
	rec := make([]byte, HeaderSize+len(key)+len(val))
	binary.LittleEndian.PutUint32(rec[4:], uint32(len(key)))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(val)))
	copy(rec[HeaderSize:], key)
	copy(rec[HeaderSize+len(key):], val)
	if guards {
		binary.LittleEndian.PutUint32(rec, crc32.ChecksumIEEE(rec[4:]))
	}
	return rec
}

// Decode reads the record at the front of b. It returns ErrNotARecord if b
// does not begin with one, which includes a truncated tail, an implausible
// length, and -- with guards -- a checksum that does not match.
func Decode(b []byte, guards bool) (key, val string, n int, err error) {
	if len(b) < HeaderSize {
		return "", "", 0, ErrNotARecord
	}
	// Both lengths are uint32 widened to int, so neither can be negative on a
	// 64-bit platform and a "< 0" check here would be dead code. What can go
	// wrong is the sum: two values near 2^32 overflow a 32-bit int, so the
	// comparison is done in uint64 where it cannot wrap.
	keyLen := uint64(binary.LittleEndian.Uint32(b[4:]))
	valLen := uint64(binary.LittleEndian.Uint32(b[8:]))
	if keyLen+valLen > MaxRecord {
		return "", "", 0, ErrNotARecord
	}
	total := HeaderSize + int(keyLen+valLen)
	if total > len(b) {
		return "", "", 0, ErrNotARecord
	}
	if guards && binary.LittleEndian.Uint32(b) != crc32.ChecksumIEEE(b[4:total]) {
		return "", "", 0, ErrNotARecord
	}
	kEnd := HeaderSize + int(keyLen)
	return string(b[HeaderSize:kEnd]), string(b[kEnd:total]), total, nil
}

// Store is an append-only log with an in-memory index.
type Store struct {
	storage Storage
	offset  int
	kv      map[string]string
	guards  bool
}

// Open replays whatever is durable and returns a Store positioned at the end
// of the last complete record. Replay stops at the first thing that is not a
// record and never attempts to resynchronise past it: a log with a damaged
// middle is truncated at the damage, not spliced back together.
func Open(storage Storage, guards bool) (*Store, error) {
	durable, err := storage.ReadAll()
	if err != nil {
		return nil, err
	}
	s := &Store{storage: storage, kv: map[string]string{}, guards: guards}
	for {
		key, val, n, err := Decode(durable[s.offset:], guards)
		if err != nil {
			return s, nil
		}
		s.kv[key] = val
		s.offset += n
	}
}

// Put appends a record. It is not durable until Commit returns.
func (s *Store) Put(key, val string) error {
	rec := Encode(key, val, s.guards)
	if err := s.storage.Write(s.offset, rec); err != nil {
		return err
	}
	s.offset += len(rec)
	s.kv[key] = val
	return nil
}

// Commit is the durability promise: once it returns, every prior Put survives
// any crash.
func (s *Store) Commit() error { return s.storage.Sync() }

// Get returns the current value for key.
func (s *Store) Get(key string) (string, bool) {
	v, ok := s.kv[key]
	return v, ok
}

// Len is the number of distinct live keys.
func (s *Store) Len() int { return len(s.kv) }

// Offset is the log position just past the last record the store accepts as
// complete. After Open it is where replay stopped.
func (s *Store) Offset() int { return s.offset }

// Snapshot copies the index, for comparing a recovered store against what the
// caller believes it wrote.
func (s *Store) Snapshot() map[string]string {
	out := make(map[string]string, len(s.kv))
	for k, v := range s.kv {
		out[k] = v
	}
	return out
}
