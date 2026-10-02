package torn

import (
	"io"
	"os"
)

// FileDisk is a real file. It exists because a durability argument made only
// against a simulated disk proves something about the simulator.
//
// Writes are buffered here rather than issued immediately, so that the page
// cache the simulator pretends to have has a real counterpart: until Sync
// calls fsync, nothing is promised. A crash is not simulated -- for that, see
// Truncate, which reproduces a torn tail on a real file by cutting it.
type FileDisk struct {
	f       *os.File
	pending []pendingWrite
}

// OpenFile opens or creates path for append-style writing.
func OpenFile(path string) (*FileDisk, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileDisk{f: f}, nil
}

func (d *FileDisk) Write(off int, b []byte) error {
	d.pending = append(d.pending, pendingWrite{off, append([]byte(nil), b...)})
	return nil
}

// Sync writes everything pending and fsyncs. Returning nil here is the
// durability promise, so the error from Sync is returned rather than logged:
// a swallowed fsync error is a lie about what is on disk.
func (d *FileDisk) Sync() error {
	for _, w := range d.pending {
		if _, err := d.f.WriteAt(w.data, int64(w.off)); err != nil {
			return err
		}
	}
	d.pending = nil
	return d.f.Sync()
}

func (d *FileDisk) ReadAll() ([]byte, error) {
	if _, err := d.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(d.f)
}

// Truncate cuts the file to n bytes, reproducing on a real file what a torn
// tail looks like after power loss. Pending writes are dropped, as they would
// be.
func (d *FileDisk) Truncate(n int64) error {
	d.pending = nil
	return d.f.Truncate(n)
}

// Size is the file's current length on disk.
func (d *FileDisk) Size() (int64, error) {
	st, err := d.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (d *FileDisk) Close() error { return d.f.Close() }
