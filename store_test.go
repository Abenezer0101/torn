package torn

import (
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct{ key, val string }{
		{"k", "v"},
		{"k", ""}, // an empty value is a value
		{"", "v"}, // and an empty key is a key
		{"", ""},  // the degenerate record
		{"key with spaces", "0123456789abc"},
		{strings.Repeat("k", 300), strings.Repeat("v", 5000)},
		{"\x00\xff", "\x00\x01\x02"}, // records are bytes, not text
	}
	for _, guards := range []bool{false, true} {
		for _, c := range cases {
			rec := Encode(c.key, c.val, guards)
			key, val, n, err := Decode(rec, guards)
			if err != nil {
				t.Fatalf("guards=%v %q=%q: %v", guards, c.key, c.val, err)
			}
			if key != c.key || val != c.val || n != len(rec) {
				t.Errorf("guards=%v round trip: got %q=%q n=%d, want %q=%q n=%d",
					guards, key, val, n, c.key, c.val, len(rec))
			}
		}
	}
}

func TestDecodeRejectsTruncation(t *testing.T) {
	// Every proper prefix of a record must be refused. This is the property
	// that makes a torn tail stop replay instead of being guessed at.
	rec := Encode("key", "value", true)
	for i := 0; i < len(rec); i++ {
		if _, _, _, err := Decode(rec[:i], true); err == nil {
			t.Errorf("accepted a %d-byte prefix of a %d-byte record", i, len(rec))
		}
	}
}

func TestDecodeRejectsCorruptionWithGuards(t *testing.T) {
	// Flip one bit in every byte position and demand the checksum notice.
	rec := Encode("key", "value", true)
	for i := range rec {
		bad := append([]byte(nil), rec...)
		bad[i] ^= 0x01
		if _, _, _, err := Decode(bad, true); err == nil {
			t.Errorf("checksum missed a flipped bit at byte %d", i)
		}
	}
}

func TestDecodeAcceptsCorruptionWithoutGuards(t *testing.T) {
	// The counterpart, and the reason the project exists: with no checksum,
	// damage to the payload is undetectable and recovery hands back a value
	// nobody wrote.
	rec := Encode("key", "value", false)
	rec[len(rec)-1] ^= 0xff
	_, val, _, err := Decode(rec, false)
	if err != nil {
		t.Fatalf("unguarded decode should not fail: %v", err)
	}
	if val == "value" {
		t.Fatal("expected the corrupted value to differ from what was written")
	}
}

func TestDecodeRejectsImplausibleLength(t *testing.T) {
	// A corrupt length field must not become a multi-gigabyte allocation.
	b := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(b[4:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(b[8:], 0xFFFFFFFF)
	if _, _, _, err := Decode(b, false); err == nil {
		t.Fatal("accepted a record claiming 8 GiB of key and value")
	}
}

func TestLengthSumCannotWrap(t *testing.T) {
	// Both lengths widen from uint32, so neither is ever negative and a
	// "< 0" check on them would be dead code. The sum is the hazard, and
	// MaxRecord is what actually bounds it.
	if uint64(^uint32(0))+uint64(^uint32(0)) <= MaxRecord {
		t.Fatal("MaxRecord no longer bounds the worst-case length sum")
	}
}

func TestAHoleReadsAsAnEmptyRecordWithoutGuards(t *testing.T) {
	// A vanished write leaves zeros. Twelve zero bytes decode as a record with
	// an empty key and an empty value -- a record nobody wrote. This is the
	// exact mechanism behind the silent corruption the hunt finds.
	zeros := make([]byte, HeaderSize)
	key, val, n, err := Decode(zeros, false)
	if err != nil || key != "" || val != "" || n != HeaderSize {
		t.Fatalf("want an empty record from a hole, got %q=%q n=%d err=%v", key, val, n, err)
	}
	// With guards the same hole is refused, because CRC32 of eight zero bytes
	// is not zero.
	if crc32.ChecksumIEEE(zeros[4:]) == 0 {
		t.Fatal("CRC32 of the zero header is zero; the guard would not catch a hole")
	}
	if _, _, _, err := Decode(zeros, true); err == nil {
		t.Fatal("guarded decode accepted a hole")
	}
}

func TestOpenReplaysWhatWasCommitted(t *testing.T) {
	disk := &SimDisk{}
	s, err := Open(disk, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"a", "3"}} {
		if err := s.Put(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(disk, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get("a"); got != "3" {
		t.Errorf("last write should win: got %q, want %q", got, "3")
	}
	if r.Len() != 2 {
		t.Errorf("want 2 live keys, got %d", r.Len())
	}
	if r.Offset() != s.Offset() {
		t.Errorf("recovery stopped at %d, writer was at %d", r.Offset(), s.Offset())
	}
}

func TestUncommittedWritesDoNotSurviveACrash(t *testing.T) {
	disk := &SimDisk{}
	s, _ := Open(disk, true)
	_ = s.Put("committed", "yes")
	_ = s.Commit()
	_ = s.Put("pending", "no")

	// No Crash call at all: nothing was synced, so nothing landed.
	r, _ := Open(disk, true)
	if _, ok := r.Get("pending"); ok {
		t.Error("an unsynced write was durable")
	}
	if got, _ := r.Get("committed"); got != "yes" {
		t.Errorf("a committed write was lost: got %q", got)
	}
}
