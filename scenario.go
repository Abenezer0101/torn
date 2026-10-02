package torn

import (
	"fmt"
	"math/rand"
	"strconv"
)

// A scenario is one randomised life of a store: some puts, some commits, then
// power loss. The workload and the invariant check live here so that the test
// suite and the trace renderer cannot drift apart -- in the first version of
// this program they were two copies of the same forty lines, and a change to
// one would have silently stopped the other from describing the same run.

const (
	recordsPerRun = 40
	keySpace      = 12
	valueAlphabet = "0123456789abc"
)

// Written is one record the caller believes it appended.
type Written struct {
	Key, Val string
	End      int // log offset just past this record
}

// Result is what a scenario did and what recovery made of it.
type Result struct {
	Seed      int64
	Guards    bool
	Log       []Written
	Committed int // records guaranteed durable by the last Commit
	Fates     []Fate
	Replayed  int // records recovery accepted; -1 if it stopped mid-record
	Offset    int // where recovery stopped reading
	Keys      int // distinct keys recovery returned

	recovered map[string]string
}

// RunScenario drives one seeded life of a store on a simulated disk and
// reports what happened. It does not judge; see [Result.Violation].
func RunScenario(seed int64, guards bool) (*Result, error) {
	rng := rand.New(rand.NewSource(seed))
	disk := &SimDisk{}
	s, err := Open(disk, guards)
	if err != nil {
		return nil, err
	}

	res := &Result{Seed: seed, Guards: guards}
	for i := 0; i < recordsPerRun; i++ {
		key := "k" + strconv.Itoa(rng.Intn(keySpace))
		val := valueAlphabet[:1+rng.Intn(len(valueAlphabet)-1)]
		if rng.Intn(2) == 0 {
			val = val[rng.Intn(len(val)):] // sometimes empty, which is a valid value
		}
		if err := s.Put(key, val); err != nil {
			return nil, err
		}
		res.Log = append(res.Log, Written{key, val, s.Offset()})
		if rng.Intn(5) == 0 {
			if err := s.Commit(); err != nil {
				return nil, err
			}
			res.Committed = len(res.Log)
		}
	}

	disk.Crash(rng)
	res.Fates = disk.Fates()

	r, err := Open(disk, guards)
	if err != nil {
		return nil, err
	}
	res.Offset = r.Offset()
	res.Keys = r.Len()
	res.Replayed = replayedCount(res.Log, res.Offset)
	res.recovered = r.Snapshot()
	return res, nil
}

// replayedCount maps a recovery offset back to a number of records, or -1 if
// the offset is not on a record boundary at all.
func replayedCount(log []Written, offset int) int {
	if offset == 0 {
		return 0
	}
	for i, e := range log {
		if e.End == offset {
			return i + 1
		}
	}
	return -1
}

// Violation returns nil if this run upheld the durability property, or the
// reason it did not.
func (r *Result) Violation() error {
	if r.Replayed < 0 {
		return fmt.Errorf("recovery stopped mid-record at offset %d: not a prefix of the log", r.Offset)
	}
	if r.Replayed < r.Committed {
		return fmt.Errorf("lost committed data: recovered %d records, %d were committed", r.Replayed, r.Committed)
	}
	want := map[string]string{}
	for _, e := range r.Log[:r.Replayed] {
		want[e.Key] = e.Val
	}
	if len(want) != len(r.recovered) {
		return fmt.Errorf("recovered %d keys, prefix of %d records holds %d", len(r.recovered), r.Replayed, len(want))
	}
	for k, v := range want {
		if got := r.recovered[k]; got != v {
			return fmt.Errorf("prefix of %d records says %q=%q, recovered %q", r.Replayed, k, v, got)
		}
	}
	return nil
}
