package engine

import "testing"

// func_ipv6_test.go — IPv6 family added 2026-08-17: parse_ipv6,
// parse_ipv6_mask, ipv6_compare, ipv6_is_match, ipv6_is_in_range,
// ipv6_is_in_any_range (func_net.go). Every worked-example value
// below is taken directly from real ADX's own documentation, fetched
// in full (not via search snippets) to resolve an ambiguity flagged
// in an earlier session — see parseIPv6WithPrefix's own doc comment
// in func_net.go for the two genuinely surprising rules found only in
// these tables' actual numeric output, not stated in any prose text.

// TestParseIPv6MaskWorkedExample guards all 7 rows of real ADX's own
// parse_ipv6_mask worked-example table exactly, including the two
// non-obvious rules: the +96 IPv4-space offset for an embedded prefix
// on a dot-bearing address, and the "::a.b.c.d" auto-canonicalization
// to the IPv4-mapped (ffff-inserted) form.
func TestParseIPv6MaskWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(ip_string:string, netmask:long)
		[
		 "192.168.255.255", 120,
		 "192.168.255.255/24", 124,
		 "255.255.255.255", 128,
		 "fe80::85d:e82c:9446:7994", 128,
		 "fe80::85d:e82c:9446:7994/120", 124,
		 "::192.168.255.255", 128,
		 "::192.168.255.255/24", 128,
		]
		| extend ip6_canonical = parse_ipv6_mask(ip_string, netmask)`)
	want := []string{
		"0000:0000:0000:0000:0000:ffff:c0a8:ff00",
		"0000:0000:0000:0000:0000:ffff:c0a8:ff00",
		"0000:0000:0000:0000:0000:ffff:ffff:ffff",
		"fe80:0000:0000:0000:085d:e82c:9446:7994",
		"fe80:0000:0000:0000:085d:e82c:9446:7900",
		"0000:0000:0000:0000:0000:ffff:c0a8:ffff",
		"0000:0000:0000:0000:0000:ffff:c0a8:ff00",
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(result.Rows))
	}
	for i, w := range want {
		got, _ := result.Rows[i][2].(string)
		if got != w {
			t.Errorf("row %d parse_ipv6_mask = %q, want %q", i, got, w)
		}
	}
}

// TestIPv6IsMatchWorkedExampleTable1 guards all 12 rows of real ADX's
// own first ipv6_is_match worked-example table, including 4 mixed
// IPv4/IPv6-notation rows — every documented result is true.
func TestIPv6IsMatchWorkedExampleTable1(t *testing.T) {
	result := queryResult(t, `datatable(ip1_string:string, ip2_string:string)
		[
		 "192.168.1.1",    "192.168.1.1",
		 "192.168.1.1/24", "192.168.1.255",
		 "192.168.1.1",    "192.168.1.255/24",
		 "192.168.1.1/30", "192.168.1.255/24",
		 "fe80::85d:e82c:9446:7994", "fe80::85d:e82c:9446:7994",
		 "fe80::85d:e82c:9446:7994/120", "fe80::85d:e82c:9446:7998",
		 "fe80::85d:e82c:9446:7994", "fe80::85d:e82c:9446:7998/120",
		 "fe80::85d:e82c:9446:7994/120", "fe80::85d:e82c:9446:7998/120",
		 "192.168.1.1",      "::ffff:c0a8:0101",
		 "192.168.1.1/24",   "::ffff:c0a8:01ff",
		 "::ffff:c0a8:0101", "192.168.1.255/24",
		 "::192.168.1.1/30", "192.168.1.255/24",
		]
		| extend result = ipv6_is_match(ip1_string, ip2_string)`)
	if len(result.Rows) != 12 {
		t.Fatalf("expected 12 rows, got %d", len(result.Rows))
	}
	for i, row := range result.Rows {
		if got := row[2].(bool); got != true {
			t.Errorf("row %d ipv6_is_match = %v, want true", i, got)
		}
	}
}

// TestIPv6IsMatchWorkedExampleTable2 guards all 9 rows of real ADX's
// own second ipv6_is_match worked-example table (with an explicit
// prefix argument) — every documented result is true.
func TestIPv6IsMatchWorkedExampleTable2(t *testing.T) {
	result := queryResult(t, `datatable(ip1_string:string, ip2_string:string, prefix:long)
		[
		 "192.168.1.1",    "192.168.1.0",   31,
		 "192.168.1.1/24", "192.168.1.255", 31,
		 "192.168.1.1",    "192.168.1.255", 24,
		 "fe80::85d:e82c:9446:7994", "fe80::85d:e82c:9446:7995",     127,
		 "fe80::85d:e82c:9446:7994/127", "fe80::85d:e82c:9446:7998", 120,
		 "fe80::85d:e82c:9446:7994/120", "fe80::85d:e82c:9446:7998", 127,
		 "192.168.1.1/24",   "::ffff:c0a8:01ff", 127,
		 "::ffff:c0a8:0101", "192.168.1.255",    120,
		 "::192.168.1.1/30", "192.168.1.255/24", 127,
		]
		| extend result = ipv6_is_match(ip1_string, ip2_string, prefix)`)
	if len(result.Rows) != 9 {
		t.Fatalf("expected 9 rows, got %d", len(result.Rows))
	}
	for i, row := range result.Rows {
		if got := row[3].(bool); got != true {
			t.Errorf("row %d ipv6_is_match(...,prefix) = %v, want true", i, got)
		}
	}
}

// TestIPv6IsInRangeWorkedExample guards real ADX's own exact 3-row
// worked example, including the genuinely discriminating third row:
// a real, reproduced bug was found and fixed here — an earlier
// version of parseIPv6WithPrefix decided the +96 IPv4-offset by
// inspecting the parsed BYTE value rather than the address TEXT, and
// wrongly rejected '0:0:0:0:0:ffff:c0a8:ac/60' (no dot anywhere in
// the text, despite its value falling in the ::ffff:0:0/96 range) as
// an out-of-range IPv4 prefix (60 > 32). This third row's documented
// result is FALSE, not true (the two addresses genuinely don't share
// a common /60 prefix) — confirmed directly from the real docs before
// writing this assertion, not assumed.
func TestIPv6IsInRangeWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(ip_address:string, ip_range:string)
		[
		 "a5e:f127:8a9d:146d:e102:b5d3:c755:abcd", "a5e:f127:8a9d:146d:e102:b5d3:c755:0000/112",
		 "a5e:f127:8a9d:146d:e102:b5d3:c755:abcd", "a5e:f127:8a9d:146d:e102:b5d3:c755:abcd",
		 "a5e:f127:8a9d:146d:e102:b5d3:c755:abcd", "0:0:0:0:0:ffff:c0a8:ac/60",
		]
		| extend result = ipv6_is_in_range(ip_address, ip_range)`)
	want := []bool{true, true, false}
	if len(result.Rows) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(result.Rows))
	}
	for i, w := range want {
		if got := result.Rows[i][2].(bool); got != w {
			t.Errorf("row %d ipv6_is_in_range = %v, want %v", i, got, w)
		}
	}
}

// TestIPv6IsInAnyRangeWorkedExample guards real ADX's own exact
// worked example (a dynamic-array Ipv6Ranges argument).
func TestIPv6IsInAnyRangeWorkedExample(t *testing.T) {
	result := queryResult(t, `let LocalNetworks=dynamic(["a5e:f127:8a9d:146d:e102:b5d3:c755:f6cd/112", "0:0:0:0:0:ffff:c0a8:ac/60"]);
		datatable(IP:string)
		[
		 "a5e:f127:8a9d:146d:e102:b5d3:c755:abcd",
		 "a5e:f127:8a9d:146d:e102:b5d3:c755:abce",
		 "a5e:f127:8a9d:146d:e102:b5d3:c755:abcf",
		 "a5e:f127:8a9d:146d:e102:b5d3:c756:abd1",
		]
		| extend IsLocal=ipv6_is_in_any_range(IP, LocalNetworks)`)
	want := []bool{true, true, true, false}
	if len(result.Rows) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(result.Rows))
	}
	for i, w := range want {
		if got := result.Rows[i][1].(bool); got != w {
			t.Errorf("row %d ipv6_is_in_any_range = %v, want %v", i, got, w)
		}
	}
}

// TestIPv6CompareBasic spot-checks ipv6_compare's ordering and
// equality contract (-1/0/1), the same shape as the already-verified
// ipv4_compare.
func TestIPv6CompareBasic(t *testing.T) {
	result := queryResult(t, `print
		lt = ipv6_compare("fe80::1", "fe80::2"),
		gt = ipv6_compare("fe80::2", "fe80::1"),
		eq = ipv6_compare("fe80::1", "fe80::1")`)
	if got := result.Rows[0][0].(int64); got != -1 {
		t.Errorf("ipv6_compare(fe80::1, fe80::2) = %v, want -1", got)
	}
	if got := result.Rows[0][1].(int64); got != 1 {
		t.Errorf("ipv6_compare(fe80::2, fe80::1) = %v, want 1", got)
	}
	if got := result.Rows[0][2].(int64); got != 0 {
		t.Errorf("ipv6_compare(fe80::1, fe80::1) = %v, want 0", got)
	}
}

// TestParseIPv6NullAndInvalidInput confirms parse_ipv6 propagates a
// null argument as null, but returns "" (empty string, real ADX's own
// documented conversion-failure convention) for a non-null but
// unparseable string.
func TestParseIPv6NullAndInvalidInput(t *testing.T) {
	result := queryResult(t, `print a = parse_ipv6(dynamic(null))`)
	if result.Rows[0][0] != nil {
		t.Errorf("parse_ipv6(null) = %v, want nil", result.Rows[0][0])
	}

	result2 := queryResult(t, `print a = parse_ipv6("not an ip")`)
	got, ok := result2.Rows[0][0].(string)
	if !ok || got != "" {
		t.Errorf("parse_ipv6(invalid) = %v, want empty string", result2.Rows[0][0])
	}
}

// TestIPv6IsMatchNullPropagation confirms a null argument propagates
// as null (as opposed to the empty-string convention used by
// parse_ipv6/parse_ipv6_mask for a non-null-but-unparseable string).
func TestIPv6IsMatchNullPropagation(t *testing.T) {
	result := queryResult(t, `print a = ipv6_is_match(dynamic(null), "fe80::1")`)
	if result.Rows[0][0] != nil {
		t.Errorf("ipv6_is_match(null, ...) = %v, want nil", result.Rows[0][0])
	}
}

