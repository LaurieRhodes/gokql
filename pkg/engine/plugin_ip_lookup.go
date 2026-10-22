package engine

// plugin_ip_lookup.go — evaluate ipv4_lookup / ipv6_lookup:
//
//	T | evaluate ipv4_lookup(LookupTable, SourceKey, LookupKey
//	    [, ExtraKey1, ..., ExtraKeyN] [, return_unmatched])
//
// A join of T to LookupTable on IP containment: a source address
// matches a lookup row whose key (an address or IP-prefix) contains
// it, and whose extra key columns equal the source row's. Output: the
// source columns, then the lookup columns other than the extra keys
// (a lookup column whose name the source already has gets a "1"
// suffix, as with lookup). return_unmatched=true keeps unmatched
// source rows with nulls.
//
// The reference does not say how overlapping prefixes resolve; every
// matching lookup row is returned, in lookup-table order, as in a join.
// The lookup table is indexed by prefix length, so each source address
// costs one probe per distinct prefix length.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["ipv4_lookup"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.applyIPLookup("ipv4_lookup", false, in, args)
	}
	evaluatePlugins["ipv6_lookup"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.applyIPLookup("ipv6_lookup", true, in, args)
	}
}

// ipKey is an address masked to a prefix length; IPv4 uses the first
// four bytes.
type ipKey struct {
	addr   [16]byte
	prefix int
}

func parseIPKey(s string, v6 bool) (ipKey, bool) {
	s = strings.TrimSpace(s)
	if v6 {
		b, p, ok := parseIPv6WithPrefix(s)
		if !ok {
			return ipKey{}, false
		}
		return ipKey{addr: maskIPv6(b, p), prefix: p}, true
	}
	n, p, ok := parseIPv4WithPrefix(s)
	if !ok {
		return ipKey{}, false
	}
	var k ipKey
	m := uint32(0)
	if p > 0 {
		m = ^uint32(0) << (32 - p)
	}
	n &= m
	k.addr[0], k.addr[1], k.addr[2], k.addr[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	k.prefix = p
	return k, true
}

// maskTo masks k's address to prefix p.
func (k ipKey) maskTo(p int, v6 bool) [16]byte {
	if v6 {
		return maskIPv6(k.addr, p)
	}
	n := uint32(k.addr[0])<<24 | uint32(k.addr[1])<<16 | uint32(k.addr[2])<<8 | uint32(k.addr[3])
	if p < 32 {
		if p == 0 {
			n = 0
		} else {
			n &= ^uint32(0) << (32 - p)
		}
	}
	var out [16]byte
	out[0], out[1], out[2], out[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	return out
}

func (e *Engine) applyIPLookup(name string, v6 bool, input *types.Table, args []string) (*types.Table, error) {
	// Trailing return_unmatched, positional or named.
	returnUnmatched := false
	if n := len(args); n > 3 {
		last := strings.TrimSpace(args[n-1])
		lv := strings.ToLower(strings.ReplaceAll(last, " ", ""))
		switch {
		case lv == "true" || lv == "false":
			returnUnmatched = lv == "true"
			args = args[:n-1]
		case strings.HasPrefix(lv, "return_unmatched="):
			v := strings.TrimPrefix(lv, "return_unmatched=")
			if v != "true" && v != "false" {
				return nil, fmt.Errorf("%s: return_unmatched must be true or false", name)
			}
			returnUnmatched = v == "true"
			args = args[:n-1]
		}
	}
	if len(args) < 3 {
		return nil, fmt.Errorf("%s(LookupTable, SourceKey, LookupKey [, ExtraKey...] [, return_unmatched]) needs at least 3 arguments", name)
	}
	stmt, err := parser.Parse(strings.TrimSpace(args[0]))
	if err != nil {
		return nil, fmt.Errorf("%s: lookup table: %w", name, err)
	}
	lookup, err := e.Execute(stmt)
	if err != nil {
		return nil, fmt.Errorf("%s: lookup table: %w", name, err)
	}
	col := func(t *types.Table, which, n string) (int, error) {
		i := t.Schema.ColumnIndex(strings.TrimSpace(n))
		if i < 0 {
			return 0, fmt.Errorf("%s: %s column %q not found", name, which, strings.TrimSpace(n))
		}
		return i, nil
	}
	srcKey, err := col(input, "source", args[1])
	if err != nil {
		return nil, err
	}
	lkKey, err := col(lookup, "lookup", args[2])
	if err != nil {
		return nil, err
	}
	var extraSrc, extraLk []int
	extraNames := map[string]bool{}
	for _, a := range args[3:] {
		si, err := col(input, "source", a)
		if err != nil {
			return nil, err
		}
		li, err := col(lookup, "lookup", a)
		if err != nil {
			return nil, err
		}
		extraSrc, extraLk = append(extraSrc, si), append(extraLk, li)
		extraNames[strings.TrimSpace(a)] = true
	}

	// Output schema.
	outSchema := types.Schema{Columns: append([]types.Column(nil), input.Schema.Columns...)}
	var lkCols []int
	for i, c := range lookup.Schema.Columns {
		if extraNames[c.Name] {
			continue
		}
		nc := c
		for outSchema.ColumnIndex(nc.Name) >= 0 {
			nc.Name += "1"
		}
		outSchema.Columns = append(outSchema.Columns, nc)
		lkCols = append(lkCols, i)
	}

	// Index the lookup rows by prefix length and masked address.
	index := map[int]map[[16]byte][]int{}
	for ri, r := range lookup.Rows {
		if r[lkKey] == nil {
			continue
		}
		k, ok := parseIPKey(fmt.Sprintf("%v", r[lkKey]), v6)
		if !ok {
			continue
		}
		if index[k.prefix] == nil {
			index[k.prefix] = map[[16]byte][]int{}
		}
		index[k.prefix][k.addr] = append(index[k.prefix][k.addr], ri)
	}
	var prefixes []int
	for p := range index {
		prefixes = append(prefixes, p)
	}
	sort.Ints(prefixes)

	out := types.NewTable(input.Name, outSchema)
	for _, sr := range input.Rows {
		var matches []int
		if sr[srcKey] != nil {
			if sk, ok := parseIPKey(fmt.Sprintf("%v", sr[srcKey]), v6); ok {
				for _, p := range prefixes {
					if p > sk.prefix {
						break // a longer prefix cannot contain a shorter one
					}
					for _, li := range index[p][sk.maskTo(p, v6)] {
						if extraKeysEqual(sr, lookup.Rows[li], extraSrc, extraLk) {
							matches = append(matches, li)
						}
					}
				}
			}
		}
		sort.Ints(matches)
		for _, li := range matches {
			row := make(types.Row, 0, len(outSchema.Columns))
			row = append(row, sr...)
			for _, c := range lkCols {
				row = append(row, lookup.Rows[li][c])
			}
			out.AddRow(row)
		}
		if len(matches) == 0 && returnUnmatched {
			row := make(types.Row, len(outSchema.Columns))
			copy(row, sr)
			out.AddRow(row)
		}
	}
	return out, nil
}

func extraKeysEqual(sr, lr types.Row, si, li []int) bool {
	for k := range si {
		a, b := sr[si[k]], lr[li[k]]
		if a == nil || b == nil || fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
			return false
		}
	}
	return true
}
