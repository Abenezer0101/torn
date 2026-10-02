package torn

import "testing"

// FuzzDecode asserts the one thing Decode must never do: misbehave on bytes it
// did not write. Random input is overwhelmingly rejected, which is correct --
// the value is in the cases that are accepted, where the returned length must
// be consistent or recovery will walk off a record boundary.
func FuzzDecode(f *testing.F) {
	f.Add(Encode("k", "v", true), true)
	f.Add(Encode("k", "v", false), false)
	f.Add(make([]byte, HeaderSize), false) // the zero hole
	f.Add([]byte{}, true)
	f.Add([]byte("\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"), false)

	f.Fuzz(func(t *testing.T, b []byte, guards bool) {
		key, val, n, err := Decode(b, guards)
		if err != nil {
			if n != 0 {
				t.Fatalf("rejected input returned n=%d, want 0", n)
			}
			return
		}
		if n < HeaderSize || n > len(b) {
			t.Fatalf("accepted a record of length %d from %d bytes", n, len(b))
		}
		if HeaderSize+len(key)+len(val) != n {
			t.Fatalf("length mismatch: header+%d+%d != %d", len(key), len(val), n)
		}
		// Re-encoding an accepted record must reproduce it. With guards that
		// is byte for byte: the checksum makes the representation canonical.
		//
		// Without guards it is byte for byte EXCEPT the first four bytes. The
		// CRC slot is still present -- both modes use the identical layout so
		// the comparison between them is honest -- but nothing reads it, so a
		// record parsed out of foreign bytes carries whatever was in that slot
		// while Encode writes zeros. The fuzzer found this within seconds of
		// being pointed at it, and the dead space is real: in unguarded mode
		// every record spends four bytes that no code path ever consults.
		got := Encode(key, val, guards)
		if guards {
			if string(got) != string(b[:n]) {
				t.Fatalf("re-encode differs: %q vs %q", got, b[:n])
			}
		} else if string(got[4:]) != string(b[4:n]) {
			t.Fatalf("re-encode differs past the CRC slot: %q vs %q", got[4:], b[4:n])
		}
	})
}

// FuzzScenario drives a whole seeded life of the store. Any seed that breaks
// the durability property is a counterexample, printed with a replay command.
func FuzzScenario(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(7))
	f.Add(int64(123456789))

	f.Fuzz(func(t *testing.T, seed int64) {
		res, err := RunScenario(seed, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Violation(); err != nil {
			t.Fatalf("seed %d: %v\n  replay: go run ./cmd/torn -trace -guards %d", seed, err, seed)
		}
	})
}
