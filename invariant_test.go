package torn

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// seedsShort is what `go test` runs; the full hunt is `go test -count=1
// -run Hunt -seeds=100000` via the CLI. Keeping the default fast matters --
// a suite nobody waits for is a suite nobody runs.
const seedsShort = 5000

func TestGuardsUpholdDurabilityAcrossSeeds(t *testing.T) {
	for seed := int64(0); seed < seedsShort; seed++ {
		res, err := RunScenario(seed, true)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if err := res.Violation(); err != nil {
			t.Fatalf("seed %d violated durability: %v\n  replay: go run ./cmd/torn -trace -guards %d",
				seed, err, seed)
		}
	}
}

// TestWithoutGuardsDurabilityBreaks pins the finding instead of describing it.
// If a change ever made the unguarded log safe, this test fails and the
// README's central claim has to be rewritten -- which is the correct outcome,
// not an inconvenience.
func TestWithoutGuardsDurabilityBreaks(t *testing.T) {
	var violations, midRecord, wrongCount, silent int
	for seed := int64(0); seed < 1000; seed++ {
		res, err := RunScenario(seed, false)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		err = res.Violation()
		if err == nil {
			continue
		}
		violations++
		switch classify(err) {
		case "mid-record":
			midRecord++
		case "count":
			wrongCount++
		default:
			silent++
		}
	}
	if violations == 0 {
		t.Fatal("no violations without checksums: the premise of this project is gone")
	}
	if silent == 0 {
		t.Error("no silently wrong values found; the worst failure mode is unrepresented")
	}
	t.Logf("1000 seeds without guards: %d violations (%d mid-record, %d wrong key count, %d silently wrong value)",
		violations, midRecord, wrongCount, silent)
}

func classify(err error) string {
	switch msg := err.Error(); {
	case contains(msg, "mid-record"):
		return "mid-record"
	case contains(msg, "keys, prefix"):
		return "count"
	default:
		return "value"
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestRealFileRecoversFromEveryTruncation is the test the first version of
// this project could not run: the durability argument held only against a
// simulated disk, which proves something about the simulator.
//
// Here the records go to a real file through a real fsync, the file is cut at
// every single byte offset, and recovery must return a prefix of the records
// every time -- never a splice, never a value nobody wrote.
func TestRealFileRecoversFromEveryTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.log")

	disk, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(disk, true)
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(1))
	var log []Written
	for i := 0; i < 50; i++ {
		key := "k" + string(rune('a'+rng.Intn(8)))
		val := valueAlphabet[:1+rng.Intn(len(valueAlphabet)-1)]
		if err := s.Put(key, val); err != nil {
			t.Fatal(err)
		}
		log = append(log, Written{key, val, s.Offset()})
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	full, err := disk.Size()
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}

	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for cut := int64(0); cut <= full; cut++ {
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Truncate(cut); err != nil {
			t.Fatal(err)
		}
		r, err := Open(d, true)
		if err != nil {
			t.Fatal(err)
		}
		n := replayedCount(log, r.Offset())
		if n < 0 {
			t.Fatalf("cut at %d: recovery stopped at offset %d, not a record boundary", cut, r.Offset())
		}
		// and the recovered state must equal the state after exactly n records
		want := map[string]string{}
		for _, e := range log[:n] {
			want[e.Key] = e.Val
		}
		got := r.Snapshot()
		if len(got) != len(want) {
			t.Fatalf("cut at %d: recovered %d keys, prefix of %d records holds %d",
				cut, len(got), n, len(want))
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("cut at %d: prefix says %q=%q, recovered %q", cut, k, v, got[k])
			}
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFileAndSimAgree holds both backends to the same outcome from the same
// writes, which is what makes the simulator's verdict transferable.
func TestFileAndSimAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agree.log")
	fileDisk, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fileDisk.Close()
	simDisk := &SimDisk{}

	for _, guards := range []bool{false, true} {
		fs, _ := Open(fileDisk, guards)
		ss, _ := Open(simDisk, guards)
		_ = fs.Put("x", "1")
		_ = ss.Put("x", "1")
		_ = fs.Commit()
		_ = ss.Commit()

		fb, err := fileDisk.ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		sb, _ := simDisk.ReadAll()
		if string(fb) != string(sb) {
			t.Errorf("guards=%v: file and sim produced different bytes", guards)
		}
	}
}

func TestCrashFatesAreRecorded(t *testing.T) {
	disk := &SimDisk{}
	s, _ := Open(disk, true)
	for i := 0; i < 20; i++ {
		_ = s.Put("k", "v")
	}
	disk.Crash(rand.New(rand.NewSource(3)))
	fates := disk.Fates()
	if len(fates) != 20 {
		t.Fatalf("want a fate per pending write, got %d for 20", len(fates))
	}
	var torn, vanished, whole int
	for _, f := range fates {
		switch {
		case f.Vanished():
			vanished++
		case f.Torn():
			torn++
		default:
			whole++
		}
	}
	if torn == 0 || vanished == 0 || whole == 0 {
		t.Errorf("seed 3 should exercise all three fates, got torn=%d vanished=%d whole=%d",
			torn, vanished, whole)
	}
}
