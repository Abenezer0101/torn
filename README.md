# torn

A durable key-value log, tested by destroying it 100,000 times.

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

100,000 seeds in 3.5s. The 24 silent ones are the interesting number: no crash,
no error, just a wrong value handed back to the caller forever.

## Run it

    go run torn.go              # hunt: 100k seeds, no checksums
    GUARDS=1 go run torn.go     # hunt: 100k seeds, checksums
    go run torn.go 4            # replay one seed
    TRACE=1 GUARDS=1 go run torn.go 7   # draw what the crash did

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

## Not here yet

No real file backend — the simulated disk is the point, `os.File` is ten lines
when it matters. No B-tree, no range scans, no compaction, values live in
memory. Single-threaded.
