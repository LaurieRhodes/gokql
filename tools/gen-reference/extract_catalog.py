#!/usr/bin/env python3
"""extract_catalog.py DOCS_QUERY_DIR > docs/reference/catalog.json

Builds the list of KQL language entries (operators, functions,
aggregations, plugins) from a checkout of MicrosoftDocs/dataexplorer-docs
(data-explorer/kusto/query): its toc.yml for the entries and their
sections, scalar-functions.md / aggregation-functions.md for categories,
and each page's first sentence for the description (CC BY 4.0,
Microsoft; see docs/reference/README.md). Support status is not decided
here: pkg/engine's TestReferenceDocs probes the engine for every entry.
"""
import json, os, re, sys

D = sys.argv[1]
toc = open(os.path.join(D, "toc.yml"), encoding="utf-8").read().splitlines()

# parse the toc into (depth, name, href) with the path of names above
items, stack = [], []
cur = None
for line in toc:
    m = re.match(r"^(\s*)- name:\s*(.*)$", line)
    if m:
        depth = (len(m.group(1)) - 2) // 4
        name = m.group(2).strip().strip('"')
        stack = stack[:depth]
        stack.append(name)
        cur = {"path": list(stack), "href": None, "display": ""}
        items.append(cur)
        continue
    m = re.match(r"^\s*href:\s*(\S+)", line)
    if m and cur is not None:
        cur["href"] = m.group(1)
    m = re.match(r"^\s*displayName:\s*(.*)$", line)
    if m and cur is not None:
        cur["display"] = m.group(1)

SECTION_KIND = {
    "Tabular operators": "operator",
    "Special functions": "function",
    "Scalar operators": "scalar-operator",
    "Scalar functions": "function",
    "Aggregation functions": "aggregation",
    "Graph": "graph",
    "Geospatial": "function",
    "Time series analysis": "function",
    "Plugins": "plugin",
    "Window functions": "window",
    "Query statements": "statement",
}

def first_sentence(href):
    p = os.path.join(D, href)
    if not os.path.exists(p):
        return ""
    txt = open(p, encoding="utf-8").read()
    txt = re.sub(r"^---.*?---", "", txt, count=1, flags=re.S)
    seen_h1 = False
    para = []
    for line in txt.splitlines():
        s = line.strip()
        if s.startswith("# "):
            seen_h1 = True
            continue
        if not seen_h1:
            continue
        if s.startswith(">") or s.startswith(":::") or s.startswith("[!") or s.startswith("<!--"):
            if para:
                break
            continue
        if not s:
            if para:
                break
            continue
        if s.startswith("#") or s.startswith("|") or s.startswith("```"):
            if para:
                break
            continue
        para.append(s)
    t = " ".join(para)
    t = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", t)  # links -> text
    t = re.sub(r"\*\*?([^*]+)\*\*?", r"\1", t)
    m = re.match(r"(.+?[.:])(\s|$)", t)
    return (m.group(1) if m else t).strip()

def category_tables(page):
    """name -> (category, description) from an at-a-glance page."""
    out = {}
    cat = None
    for line in open(os.path.join(D, page), encoding="utf-8"):
        m = re.match(r"^##+\s+(.*)$", line)
        if m:
            cat = m.group(1).strip()
            continue
        m = re.match(r"^\|\s*\[([^\]]+)\]\(([^)]+)\)\s*\|\s*(.*?)\s*\|?\s*$", line)
        if m and cat:
            name = m.group(1).replace("()", "").strip()
            desc = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", m.group(3)).strip().rstrip("|").strip()
            out.setdefault(name, (cat, desc))
    return out

scalar_cats = category_tables("scalar-functions.md")
agg_cats = category_tables("aggregation-functions.md")

def clean_name(n):
    n = n.strip()
    for suf in [" operator", " plugin", " function", " statement"]:
        if n.endswith(suf):
            n = n[: -len(suf)]
    n = n.replace("()", "").strip()
    return n

expanded = []
for it in items:
    raw = it["path"][-1]
    names = [n.strip() for n in raw.split(", ")] if re.fullmatch(r"[\w]+\(\)(, [\w]+\(\))+", raw) else [raw]
    names += [d for d in re.findall(r"(?:^|[,\s])(\w+\(\))", it.get("display", "")) if not re.search(r"\w \w+\(\)", it.get("display", "") or "") if d not in names and "()" in raw]
    for n in names:
        expanded.append(dict(it, path=it["path"][:-1] + [n]))
items = expanded

entries = {}
for it in items:
    if not it["href"] or it["href"].startswith(("http://", "https://")) or "/" in it["href"]:
        continue
    sec = it["path"][0]
    kind = SECTION_KIND.get(sec)
    if not kind:
        continue
    raw = it["path"][-1]
    if re.search(r"overview$", raw, re.I) and len(it["path"]) > 2 and it["path"][-2].endswith(" operator"):
        raw = it["path"][-2]  # "summarize operator > summarize operator overview"
    if raw not in ("join operator", "render operator") and any(p in ("join operator", "render operator") for p in it["path"][:-1]):
        continue  # join flavors, render visualizations
    if raw.endswith(" operator") and kind not in ("scalar-operator",):
        kind = "operator"
    name = clean_name(raw)
    name = re.sub(r"\s*\(.*\)$", "", name)  # "!= (not equals)"
    if kind == "plugin":
        name = name.lower()
    if kind == "scalar-operator" and name in ("Bitwise binary operators", "Datetime/timespan arithmetic",
                                              "Logical or binary operators", "Numerical operators"):
        continue
    # skip overview pages
    if " " in name and kind not in ("scalar-operator", "statement"):
        continue
    if re.search(r"overview|types|at a glance|semantics|best practices", raw, re.I):
        continue
    if kind == "graph":
        kind = "operator" if not raw.endswith("()") else "function"
        if raw.endswith("()") is False and "operator" not in raw and "-" not in name:
            kind = "function"
    if kind == "operator" and name.startswith(("join flavors", "Cross-cluster")):
        continue
    cat = it["path"][-2] if len(it["path"]) > 2 else sec
    if cat.endswith(" operator") or cat in ("Operators",):
        cat = "Graph operators" if sec == "Graph" else sec
    desc = ""
    if kind == "function" and name in scalar_cats:
        cat, desc = scalar_cats[name]
    if kind == "aggregation" and name in agg_cats:
        cat, desc = agg_cats[name]
    if kind == "function" and name not in scalar_cats:
        cat = {"Scalar functions": "Other scalar functions", "Graph": "Graph functions",
               "Time series analysis": "Series processing functions"}.get(sec, sec)
    if not desc:
        desc = first_sentence(it["href"])
    if not desc:  # pages that open with an include
        desc = {"python": "Runs a user-defined function using a Python script."}.get(name, "")
    key = (kind, name)
    if key in entries:
        continue
    entries[key] = {
        "kind": kind,
        "name": name,
        "category": cat,
        "description": desc,
        "url": "https://learn.microsoft.com/kusto/query/" + it["href"][:-3],
    }

out = sorted(entries.values(), key=lambda e: (e["kind"], e["category"], e["name"]))
json.dump(out, sys.stdout, indent=1, ensure_ascii=False)
print()
