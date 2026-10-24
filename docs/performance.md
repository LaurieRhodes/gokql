# Performance

What to expect from gokql on one machine, measured with the scripts in
[`tools/bench/`](../tools/bench/). Every query runs in a fresh
`gokql` process, as an agent or a script would run it, so each time
includes starting the process, discovering the database and opening
its files. Each figure is the median of five runs.

| | |
|---|---|
| Machine | Intel Core i9-13900HX (32 threads), 32 GB RAM, NVMe SSD; a working laptop, not idle |
| Build | gokql at `de9a10a` plus the two fixes noted below, Go 1.24.9, Linux |
| Binary | 24.7 MB with debug information; needs only the system C library (`CGO_ENABLED=0` builds a fully static one) |
| Data | `Events`: 10,000,000 synthetic rows (time, host, user, action, bytes, status, message), 10 extents |

These are gokql's own figures. They are not a comparison with Azure
Data Explorer or the Kusto emulator, whose licence does not permit
benchmarking.

## Storage and loading

| | |
|---|---|
| 10 million rows on disk | 578 MB (about 61 bytes a row, each with a unique message string) |
| Generating and writing them with KQL (`gen.sh`, ten `.set-or-append` commands) | 52.5 s, about 190,000 rows/s |
| `.ingest into` a 1-million-row CSV (88 MB) | 2.2 s, about 460,000 rows/s; 58 MB on disk |
| Peak memory while writing 1 million rows | 1.0–1.2 GB |

## Queries over 10 million rows

| Query | Time | Peak memory |
|---|---|---|
| `print 1` (process start, nothing to open) | 0.00 s | 16 MB |
| `Events \| count` | 0.01 s | 15 MB |
| `where Time between (datetime(2026-03-01) .. 1d) \| count` | 0.01 s | 19 MB |
| `where Time between (... .. 7d) and Status == 500 \| summarize count() by Action` | 0.02 s | 24 MB |
| `where Status == 500 \| count` | 0.03 s | 225 MB |
| `summarize count() by Action` (8 groups) | 0.04 s | 237 MB |
| `summarize count(), avg(Bytes) by Host` (1,000 groups) | 0.05 s | 247 MB |
| `where Status == 500 \| summarize by Host \| join (Hosts) \| summarize by Region` | 0.05 s | 249 MB |
| `summarize dcount(User)` (10,000 values) | 0.07 s | 249 MB |
| `summarize sum(Bytes), dcount(User) by Host \| top 10 by ...` | 0.10 s | 266 MB |
| `where Message has "timeout" \| count` | 0.16 s | 52 MB |
| `where User == "user42" \| project Time, Action \| take 10` | 1.02 s | 2.5 GB |
| `summarize count() by bin(Time, 1d)` | 1.89 s | 2.1 GB |
| `summarize arg_max(Time, *) by User` (10,000 groups) | 2.51 s | 4.5 GB |
| `summarize percentiles(Bytes, 50, 95, 99)` | 5.47 s | 2.2 GB |

On a small table the process start dominates: the StormEvents sample
(59,066 rows) answers `summarize count(), sum(DamageProperty) by State
| top 5` in 0.07 s using 36 MB.

## Reading the numbers

Two execution paths explain the spread:

- **Columnar.** A pipeline of `where` filters on columns and a
  `summarize` (or `count`) of `count`, `sum`, `avg`, `min`, `max` and
  `dcount` by plain columns runs directly on the compressed columns,
  in parallel across extents. Time ranges and other numeric filters
  skip whole zones using their min/max statistics, which is why a
  one-day filter over ten million rows reads almost nothing. These
  queries take tens of milliseconds and a few hundred megabytes at
  most.
- **Row engine.** Everything else materialises the rows it needs and
  evaluates them one by one: grouping by an expression such as
  `bin(Time, 1d)`, `arg_max`, percentiles, and filters followed by
  `project`. At ten million rows these take seconds and gigabytes.
  Filter first, or summarize to fewer rows, and they are fast again;
  the same queries over a million rows take a tenth of the time.

Moving `bin()` grouping, `arg_max` and percentiles onto the columnar
path is the obvious next work. Two fixes were made while preparing
this page: `between` now uses the columnar path and zone statistics
(it previously took 1.65 s for the one-day filter), and percentiles
sort without reflection (14.1 s before).

## Reproducing

```bash
cd tools/bench
GOKQL=$(pwd)/../../gokql ./gen.sh /tmp/benchdb 10        # 10 x 1,000,000 rows
gokql -db /tmp/benchdb -e '.set-or-append Hosts <| range i from 0 to 999 step 1
| project Host = strcat("host-", tostring(i)), Region = tostring(dynamic(["au","us","eu","asia"])[i % 4])'
GOKQL=$(pwd)/../../gokql ./bench.sh /tmp/benchdb          # name, median seconds, peak MB
```
