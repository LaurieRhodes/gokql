#!/bin/sh
# bench.sh DB: each query in a fresh gokql process, 5 runs; prints the median wall time and the largest peak RSS
G=${GOKQL:-gokql}
run() {
  name="$1"; q="$2"
  times=""; rss=0
  for r in 1 2 3 4 5; do
    out=$( { /usr/bin/time -f "%e %M" $G -no-rc -readonly -db "$DB" -mode csv -e "$q" > /dev/null; } 2>&1 | tail -1)
    t=${out% *}; m=${out#* }
    times="$times $t"; [ "$m" -gt "$rss" ] && rss=$m
  done
  med=$(echo $times | tr ' ' '\n' | sort -n | sed -n 3p)
  printf '%s\t%s\t%s\n' "$name" "$med" "$((rss / 1024))"
}
DB="$1"
run "startup: print 1"                       'print 1'
run "count"                                  'Events | count'
run "filter: Status == 500"                  'Events | where Status == 500 | count'
run "filter: one day by time"                'Events | where Time between (datetime(2026-03-01) .. 1d) | count'
run "filter: one user, take 10"              'Events | where User == "user42" | project Time, Action | take 10'
run "text search: has"                       'Events | where Message has "timeout" | count'
run "summarize by Action (8 groups)"         'Events | summarize count() by Action'
run "summarize by Host (1,000 groups)"       'Events | summarize count(), avg(Bytes) by Host'
run "dcount of User (10,000 values)"         'Events | summarize dcount(User)'
run "filter + summarize, one week"           'Events | where Time between (datetime(2026-03-01) .. 7d) and Status == 500 | summarize count() by Action'
run "summarize by day (bin)"                 'Events | summarize count() by bin(Time, 1d) | count'
run "sum + dcount by Host, top 10"           'Events | summarize Bytes = sum(Bytes), Users = dcount(User) by Host | top 10 by Bytes'
run "latest row per User (10,000 groups)"    'Events | summarize arg_max(Time, *) by User | count'
run "join errors to 1,000 hosts"             'Events | where Status == 500 | summarize Errors = count() by Host | join kind=inner (Hosts) on Host | summarize sum(Errors) by Region'
run "percentiles of Bytes"                   'Events | summarize percentiles(Bytes, 50, 95, 99)'
