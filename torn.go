// torn: a durable key-value log that is tested by destroying it.
//
// Every byte reaches storage through one interface, so a seeded PRNG can
// simulate what real hardware does on power loss: pending writes land fully,
// land partially (a "torn" write), or vanish. One 64-bit seed replays any
// failure exactly.
package main

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"strconv"
)

// ---------- storage: the single injection point ----------

type pending struct {
	off  int
	data []byte
}

type disk struct {
	durable []byte    // survived the last sync; a crash cannot take it away
	cache   []pending // in the OS page cache; a crash can do anything to it
	fates   []fate    // what the crash did to each cached write, for the trace
}

// fate of one cached write: n < 0 vanished, n == len(data) landed whole,
// otherwise only the first n bytes reached the platter.
type fate struct{ n, size int }

func (d *disk) write(off int, b []byte) {
	d.cache = append(d.cache, pending{off, append([]byte(nil), b...)})
}

func (d *disk) sync() {
	for _, w := range d.cache {
		d.land(w.off, w.data)
	}
	d.cache = nil
}

func (d *disk) land(off int, b []byte) {
	for len(d.durable) < off+len(b) {
		d.durable = append(d.durable, 0) // a hole in the file reads as zeros
	}
	copy(d.durable[off:], b)
}

// crash is power loss. Each cached write independently vanishes, lands torn,
// or lands whole — which also reorders them, since a later write can survive
// an earlier one.
func (d *disk) crash(rng *rand.Rand) {
	for _, w := range d.cache {
		n := -1
		switch rng.Intn(3) {
		case 0: // vanishes
		case 1: // torn: only a prefix reached the platter
			n = rng.Intn(len(w.data))
			d.land(w.off, w.data[:n])
		case 2:
			n = len(w.data)
			d.land(w.off, w.data)
		}
		d.fates = append(d.fates, fate{n, len(w.data)})
	}
	d.cache = nil
}

// ---------- the store ----------

const header = 12 // crc32 | keylen | vallen

const digits = "0123456789abc"

type store struct {
	d      *disk
	off    int
	kv     map[string]string
	guards bool // v1: checksum every record and stop reading at the first bad one
}

func encode(k, v string, guards bool) []byte {
	rec := make([]byte, header+len(k)+len(v))
	binary.LittleEndian.PutUint32(rec[4:], uint32(len(k)))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(v)))
	copy(rec[header:], k)
	copy(rec[header+len(k):], v)
	if guards {
		binary.LittleEndian.PutUint32(rec, crc32.ChecksumIEEE(rec[4:]))
	}
	return rec
}

// decode returns the record at the front of b, or ok=false if b does not
// begin with one.
func decode(b []byte, guards bool) (k, v string, n int, ok bool) {
	if len(b) < header {
		return "", "", 0, false
	}
	kl := int(binary.LittleEndian.Uint32(b[4:]))
	vl := int(binary.LittleEndian.Uint32(b[8:]))
	if kl < 0 || vl < 0 || kl+vl > len(b)-header {
		return "", "", 0, false
	}
	n = header + kl + vl
	if guards && binary.LittleEndian.Uint32(b) != crc32.ChecksumIEEE(b[4:n]) {
		return "", "", 0, false
	}
	return string(b[header : header+kl]), string(b[header+kl : n]), n, true
}

func (s *store) put(k, v string) {
	rec := encode(k, v, s.guards)
	s.d.write(s.off, rec)
	s.off += len(rec)
	s.kv[k] = v
}

// commit is the durability promise: once it returns, every prior put survives
// any crash.
func (s *store) commit() { s.d.sync() }

func open(d *disk, guards bool) *store {
	s := &store{d: d, kv: map[string]string{}, guards: guards}
	for {
		k, v, n, ok := decode(d.durable[s.off:], guards)
		if !ok {
			return s
		}
		s.kv[k] = v
		s.off += n
	}
}

// ---------- the simulator ----------

// A correct append-only log has one durability property: after a crash,
// recovery returns some PREFIX of the records that were written, and that
// prefix reaches at least as far as the last commit. Anything else is a lost
// write, a resurrected write, or corruption.
func trial(seed int64, guards bool) error {
	rng := rand.New(rand.NewSource(seed))
	d := &disk{}
	s := open(d, guards)

	type rec struct {
		k, v string
		end  int // log offset just past this record
	}
	var log []rec
	committed := 0 // records guaranteed durable

	for i := 0; i < 40; i++ {
		k := "k" + strconv.Itoa(rng.Intn(12))
		v := digits[:1+rng.Intn(len(digits)-1)]
		if rng.Intn(2) == 0 {
			v = v[rng.Intn(len(v)):]
		}
		s.put(k, v)
		log = append(log, rec{k, v, s.off})
		if rng.Intn(5) == 0 {
			s.commit()
			committed = len(log)
		}
	}
	d.crash(rng)

	r := open(d, guards)

	// Which prefix did recovery claim? Its read offset must land exactly on a
	// record boundary.
	n := -1
	if r.off == 0 {
		n = 0
	}
	for i, e := range log {
		if e.end == r.off {
			n = i + 1
		}
	}
	if n < 0 {
		return fmt.Errorf("recovery stopped mid-record at offset %d: not a prefix of the log", r.off)
	}
	if n < committed {
		return fmt.Errorf("lost committed data: recovered %d records, %d were committed", n, committed)
	}

	want := map[string]string{}
	for _, e := range log[:n] {
		want[e.k] = e.v
	}
	if len(want) != len(r.kv) {
		return fmt.Errorf("recovered %d keys, prefix of %d records holds %d", len(r.kv), n, len(want))
	}
	for k, v := range want {
		if r.kv[k] != v {
			return fmt.Errorf("prefix of %d records says %q=%q, recovered %q", n, k, v, r.kv[k])
		}
	}
	return nil
}

// trace redraws one seed as a picture: what each record's write suffered, and
// where recovery drew the line.
func trace(seed int64, guards bool) {
	rng := rand.New(rand.NewSource(seed))
	d := &disk{}
	s := open(d, guards)
	type rec struct {
		k, v string
		end  int
	}
	var log []rec
	committed := 0
	for i := 0; i < 40; i++ {
		k := "k" + strconv.Itoa(rng.Intn(12))
		v := digits[:1+rng.Intn(len(digits)-1)]
		if rng.Intn(2) == 0 {
			v = v[rng.Intn(len(v)):]
		}
		s.put(k, v)
		log = append(log, rec{k, v, s.off})
		if rng.Intn(5) == 0 {
			s.commit()
			committed = len(log)
		}
	}
	d.crash(rng)
	r := open(d, guards)

	fmt.Printf("seed %d  guards=%v  %d records, last commit after #%02d\n\n", seed, guards, len(log), committed-1)
	for i, e := range log {
		bar, note := "████████", "durable"
		if i >= committed {
			f := d.fates[i-committed]
			switch {
			case f.n < 0:
				bar, note = "░░░░░░░░", "vanished"
			case f.n == f.size:
				bar, note = "████████", "landed anyway"
			default:
				full := f.n * 8 / f.size
				bar = "████████"[:3*full] + "░░░░░░░░"[:3*(8-full)]
				note = fmt.Sprintf("TORN %d/%d bytes", f.n, f.size)
			}
		}
		mark := "  "
		if i == committed {
			mark = "<-" // power cut here
		}
		fmt.Printf("#%02d %-4s = %-13s %s %-16s %s\n", i, e.k, e.v, bar, note, mark)
	}
	n := 0
	for i, e := range log {
		if e.end == r.off {
			n = i + 1
		}
	}
	fmt.Printf("\nrecovery: replayed %d of %d records, %d keys\n", n, len(log), len(r.kv))
}

func main() {
	if os.Getenv("TRACE") == "1" {
		seed, _ := strconv.ParseInt(os.Args[1], 10, 64)
		trace(seed, os.Getenv("GUARDS") == "1")
		return
	}
	guards := os.Getenv("GUARDS") == "1"
	if len(os.Args) > 1 { // replay one seed
		seed, _ := strconv.ParseInt(os.Args[1], 10, 64)
		fmt.Printf("seed %d guards=%v -> %v\n", seed, guards, trial(seed, guards))
		return
	}
	for seed := int64(0); seed < 100000; seed++ {
		if err := trial(seed, guards); err != nil {
			fmt.Printf("FAIL after %d seeds\n  seed %d: %v\n  replay: go run torn.go %d\n", seed, seed, err, seed)
			os.Exit(1)
		}
	}
	fmt.Printf("100000 seeds, no durability violation (guards=%v)\n", guards)
}
