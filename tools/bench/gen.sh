#!/bin/sh
# gen.sh DB BATCHES: BATCHES x 1,000,000 synthetic event rows
G=${GOKQL:-gokql}
for b in $(seq 0 $(($2 - 1))); do
  $G -no-rc -db "$1" -e ".set-or-append Events <| range i from $((b * 1000000 + 1)) to $(((b + 1) * 1000000)) step 1
| extend Time = datetime(2026-01-01) + i * 3s,
    Host = strcat(\"host-\", tostring(hash(i, 1000))),
    User = strcat(\"user\", tostring(hash(i * 7, 10000))),
    Action = tostring(dynamic([\"login\",\"logout\",\"read\",\"write\",\"delete\",\"list\",\"upload\",\"download\"])[hash(i * 13, 8)]),
    Bytes = hash(i * 17, 1000000),
    Status = iff(hash(i * 19, 100) < 3, 500, 200),
    Message = strcat(\"request \", tostring(i), \" from host-\", tostring(hash(i, 1000)), iff(hash(i * 23, 1000) == 0, \" timeout exceeded\", \" ok\"))
| project-away i" > /dev/null || exit 1
done
