// Command torn hunts for durability violations, replays one seed, or draws
// what a crash did.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/Abenezer0101/torn"
)

func main() {
	var (
		guards = flag.Bool("guards", false, "checksum every record and stop replay at the first bad one")
		seeds  = flag.Int64("seeds", 100000, "how many seeds to hunt through")
		trace  = flag.Bool("trace", false, "draw one seed instead of hunting (requires a seed argument)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `torn -- a durable log, tested by destroying it

  torn                       hunt 100k seeds without checksums (expect a failure)
  torn -guards               hunt 100k seeds with checksums
  torn -guards 4             replay one seed
  torn -trace -guards 7      draw what the crash did to one seed

`)
		flag.PrintDefaults()
	}
	flag.Parse()

	// The original took the seed from os.Args[1] unconditionally, so asking
	// for a trace without one panicked with an index out of range. A tool that
	// crashes on its own usage error is not finished.
	var seed int64
	haveSeed := flag.NArg() > 0
	if haveSeed {
		parsed, err := strconv.ParseInt(flag.Arg(0), 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "torn: %q is not a seed: %v\n", flag.Arg(0), err)
			os.Exit(2)
		}
		seed = parsed
	}

	switch {
	case *trace && !haveSeed:
		fmt.Fprintln(os.Stderr, "torn: -trace needs a seed, e.g. torn -trace -guards 7")
		flag.Usage()
		os.Exit(2)
	case *trace:
		os.Exit(runTrace(seed, *guards))
	case haveSeed:
		os.Exit(replay(seed, *guards))
	default:
		os.Exit(hunt(*seeds, *guards))
	}
}

func hunt(seeds int64, guards bool) int {
	for seed := int64(0); seed < seeds; seed++ {
		res, err := torn.RunScenario(seed, guards)
		if err != nil {
			fmt.Fprintf(os.Stderr, "torn: seed %d: %v\n", seed, err)
			return 1
		}
		if v := res.Violation(); v != nil {
			fmt.Printf("FAIL after %d seeds\n  seed %d: %v\n  replay: torn -trace%s %d\n",
				seed, seed, v, guardFlag(guards), seed)
			return 1
		}
	}
	fmt.Printf("%d seeds, no durability violation (guards=%v)\n", seeds, guards)
	return 0
}

func replay(seed int64, guards bool) int {
	res, err := torn.RunScenario(seed, guards)
	if err != nil {
		fmt.Fprintf(os.Stderr, "torn: %v\n", err)
		return 1
	}
	if v := res.Violation(); v != nil {
		fmt.Printf("seed %d guards=%v -> %v\n", seed, guards, v)
		return 1
	}
	fmt.Printf("seed %d guards=%v -> ok\n", seed, guards)
	return 0
}

func guardFlag(guards bool) string {
	if guards {
		return " -guards"
	}
	return ""
}

func runTrace(seed int64, guards bool) int {
	res, err := torn.RunScenario(seed, guards)
	if err != nil {
		fmt.Fprintf(os.Stderr, "torn: %v\n", err)
		return 1
	}

	cut := "never"
	if res.Committed > 0 {
		cut = fmt.Sprintf("#%02d", res.Committed-1)
	}
	fmt.Printf("seed %d  guards=%v  %d records, last commit after %s\n\n",
		res.Seed, res.Guards, len(res.Log), cut)

	for i, e := range res.Log {
		bar, note := "████████", "durable"
		if i >= res.Committed {
			f := res.Fates[i-res.Committed]
			switch {
			case f.Vanished():
				bar, note = "░░░░░░░░", "vanished"
			case f.Torn():
				full := f.N * 8 / f.Size
				bar = "████████"[:3*full] + "░░░░░░░░"[:3*(8-full)]
				note = fmt.Sprintf("TORN %d/%d bytes", f.N, f.Size)
			default:
				bar, note = "████████", "landed anyway"
			}
		}
		mark := "  "
		if i == res.Committed {
			mark = "<-" // power cut here
		}
		fmt.Printf("#%02d %-4s = %-13s %s %-16s %s\n", i, e.Key, e.Val, bar, note, mark)
	}

	fmt.Printf("\nrecovery: replayed %d of %d records, %d keys\n",
		max(res.Replayed, 0), len(res.Log), res.Keys)
	if v := res.Violation(); v != nil {
		fmt.Printf("VIOLATION: %v\n", v)
		return 1
	}
	fmt.Println("no durability violation")
	return 0
}
