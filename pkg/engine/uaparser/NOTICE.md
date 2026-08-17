# Third-party data notice

`regexes.yaml` in this directory is taken verbatim from the
[ua-parser/uap-core](https://github.com/ua-parser/uap-core) project
(fetched 2026-08-18), which real ADX's own `parse_user_agent()`
function is documented as being "built on regex checks of the input
string against a huge number of predefined patterns" from.

Per the upstream project's own README: "The data contained in
regexes.yaml is Copyright 2009 Google Inc. and available under the
Apache License, Version 2.0." See `LICENSE-APACHE-2.0.txt` in this
directory for the full license text.

This engine (gokql) does not modify the contents of `regexes.yaml`.
The interpreter that reads it (`uaparser.go`) is original code
written for this project, implementing the algorithm described in
ua-parser/uap-core's own
[specification.md](https://github.com/ua-parser/uap-core/blob/master/docs/specification.md).
