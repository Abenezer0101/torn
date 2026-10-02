package torn

import "math/rand"

// SimDisk is a disk that can lose power. Writes queue in a cache that stands
// in for the OS page cache; Sync moves them to durable storage; Crash decides
// each cached write's fate independently.
//
// Independence is what makes it useful. A later write surviving while an
// earlier one vanishes reorders the log, and that reordering -- not the torn
// write itself -- is what produces the silent corruption these tests hunt for.
type SimDisk struct {
	durable []byte
	cache   []pendingWrite
	fates   []Fate
}

type pendingWrite struct {
	off  int
	data []byte
}

// Fate records what a crash did to one cached write: N < 0 vanished,
// N == Size landed whole, otherwise only the first N bytes reached the platter.
type Fate struct{ N, Size int }

// Torn reports whether the write landed as an incomplete prefix.
func (f Fate) Torn() bool { return f.N >= 0 && f.N < f.Size }

// Vanished reports whether the write left no trace.
func (f Fate) Vanished() bool { return f.N < 0 }

func (d *SimDisk) Write(off int, b []byte) error {
	d.cache = append(d.cache, pendingWrite{off, append([]byte(nil), b...)})
	return nil
}

func (d *SimDisk) Sync() error {
	for _, w := range d.cache {
		d.land(w.off, w.data)
	}
	d.cache = nil
	return nil
}

func (d *SimDisk) ReadAll() ([]byte, error) { return d.durable, nil }

func (d *SimDisk) land(off int, b []byte) {
	for len(d.durable) < off+len(b) {
		d.durable = append(d.durable, 0) // a hole in a file reads as zeros
	}
	copy(d.durable[off:], b)
}

// Crash is power loss. Every cached write independently vanishes, lands as a
// prefix, or lands whole.
func (d *SimDisk) Crash(rng *rand.Rand) {
	for _, w := range d.cache {
		n := -1
		switch rng.Intn(3) {
		case 0: // vanishes
		case 1:
			n = rng.Intn(len(w.data))
			d.land(w.off, w.data[:n])
		case 2:
			n = len(w.data)
			d.land(w.off, w.data)
		}
		d.fates = append(d.fates, Fate{n, len(w.data)})
	}
	d.cache = nil
}

// Fates returns what the last Crash did to each write that was pending, in
// the order they were issued.
func (d *SimDisk) Fates() []Fate { return d.fates }
