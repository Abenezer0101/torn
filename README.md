# torn

A durable key-value log, tested by destroying it.

Every byte reaches storage through one interface, so a seeded PRNG can do what
real hardware does on power loss: a pending write lands whole, lands as a torn
prefix, or vanishes — which also reorders writes, since a later one can survive
an earlier one. One 64-bit seed replays any failure exactly.

The invariant is the only one an append-only log owes you: **after a crash,
recovery returns a prefix of the records that were written, reaching at least
as far as the last commit.** Lost writes, resurrected writes and silent
corruption are all violations of that one sentence.

## Result

Same engine, same seeds. The only difference is a 4-byte checksum per record
and stopping recovery at the first record that fails it.

| | 1,000 seeds | failure modes |
|---|---|---|
| no checksums | **555 violations** | 487 recovery landed mid-record, 44 wrong key count, **24 silently recovered a value nobody wrote** |
| checksums | 0 violations | — |

100,000 seeds in 4.1s. The 24 silent ones are the interesting number: no crash,
no error, just a wrong value handed back to the caller forever.

Beyond the fixed seeds, `FuzzScenario` has driven 1.15M generated seeds and
`FuzzDecode` 5.2M generated inputs without finding a violation.

## It also runs on a real file

A durability argument made only against a simulated disk proves something
about the simulator. `FileDisk` is an `os.File` behind the same `Storage`
interface, with a real `fsync`, and
`TestRealFileRecoversFromEveryTruncation` writes 50 records, then cuts the
file at **every single byte offset** and demands that recovery return a prefix
of the records every time — never a splice, never a value nobody wrote.

## Run it

    go test ./...                        # unit tests, 5k seeds, every truncation
    go test -race ./...                  # same, under the race detector
    go run ./cmd/torn                    # hunt 100k seeds, no checksums
    go run ./cmd/torn -guards            # hunt 100k seeds, checksums
    go run ./cmd/torn -guards 4          # replay one seed
    go run ./cmd/torn -trace -guards 7   # draw what the crash did

    go test -fuzz FuzzScenario           # look for a seed the fixed range misses
    go test -fuzz FuzzDecode             # look for bytes that break the parser

Open `trace.html` for the same traces in a browser: eight seeds, a checksum
toggle, and the three bands redrawing as you flip it.

```
#28 k6   = 0             ████████ durable
#29 k7   = 01            ░░░░░░░░ vanished         <- power cut here
#30 k7   = 456789        ████████ landed anyway
#32 k7   = 67            ██████░░ TORN 12/16 bytes
```

Record 30 surviving while 29 vanished is the hazard: without checksums,
recovery reads 29's missing bytes, finds 30's header where a payload should be,
and parses the splice as a valid record.

## How a hole becomes a record

A vanished write leaves zeros, and twelve zero bytes are a syntactically
perfect record: key length zero, value length zero, so recovery accepts an
empty key with an empty value and walks forward twelve bytes into whatever
follows. CRC32 of eight zero bytes is 1696784233, not 0, so the checksummed
reader refuses the same hole. `TestAHoleReadsAsAnEmptyRecordWithoutGuards`
pins both halves.

## Layout

```
store.go              Storage, Encode/Decode, Store, Open
sim.go                SimDisk: the disk that can lose power
file.go               FileDisk: a real os.File behind the same interface
scenario.go           one seeded life of a store, and the invariant check
store_test.go         encode/decode, truncation, corruption, replay
invariant_test.go     the seed hunt, and every truncation of a real file
fuzz_test.go          FuzzDecode, FuzzScenario
cmd/torn/             the CLI
trace.html            the traces in a browser
```

The workload and the invariant check live in `scenario.go` precisely once. In
the first version they were two copies of the same forty lines, one in the
hunt and one in the trace renderer, and a change to either would have silently
stopped them describing the same run.

## Not here yet

No B-tree, no range scans, no compaction. The index is a map, so it is
rebuilt in full on open and bounded by memory. Single-threaded: `Store` has no
locking and is not safe for concurrent use.

The CRC slot is present in both modes so the comparison between them is
honest, which means that with checksums off every record carries four bytes no
code path ever reads. `FuzzDecode` found that within seconds of being pointed
at it.
