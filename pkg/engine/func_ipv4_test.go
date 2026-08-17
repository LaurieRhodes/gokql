package engine

import "testing"

// func_ipv4_test.go — IPv4 text-match, range, and mask function
// family added 2026-08-17 (has_any_ipv4, has_ipv4_prefix,
// has_any_ipv4_prefix, ipv4_is_in_any_range, ipv4_is_match,
// ipv4_range_to_cidr_list, ipv4_netmask_suffix, format_ipv4_mask,
// parse_ipv4_mask), plus regression coverage for five real,
// pre-existing bugs found and fixed in already-implemented sibling
// functions (has_ipv4, format_ipv4, ipv4_compare, ipv4_is_private,
// ipv4_is_in_range) while verifying this family against real ADX
// docs. Every worked-example value below is taken directly from
// real ADX's own documentation, not invented.

// --- has_ipv4 delimiting bug regression ---

func TestHasIPv4RequiresProperDelimiting(t *testing.T) {
	// Real bug: has_ipv4 previously did a plain strings.Contains with
	// no boundary check, so an IP glued to preceding digits matched
	// when it must not.
	result := queryResult(t, `print
		improper = has_ipv4('05:04:54127.0.0.1 GET /favicon.ico 404', '127.0.0.1'),
		proper = has_ipv4('05:04:54 127.0.0.1 GET /favicon.ico 404', '127.0.0.1'),
		invalid = has_ipv4('05:04:54 127.0.0.256 GET /favicon.ico 404', '127.0.0.256')`)
	if result.Rows[0][0].(bool) != false {
		t.Errorf("has_ipv4 improperly-delimited = %v, want false", result.Rows[0][0])
	}
	if result.Rows[0][1].(bool) != true {
		t.Errorf("has_ipv4 properly-delimited = %v, want true", result.Rows[0][1])
	}
	if result.Rows[0][2].(bool) != false {
		t.Errorf("has_ipv4 invalid address = %v, want false", result.Rows[0][2])
	}
}

// --- has_any_ipv4 ---

func TestHasAnyIPv4WorkedExamples(t *testing.T) {
	result := queryResult(t, `print
		multiScalar = has_any_ipv4('05:04:54 127.0.0.1 GET /favicon.ico 404', '127.0.0.1', '127.0.0.2'),
		dynArray = has_any_ipv4('05:04:54 127.0.0.1 GET /favicon.ico 404', dynamic(['127.0.0.1', '127.0.0.2'])),
		invalidIP = has_any_ipv4('05:04:54 127.0.0.256 GET /favicon.ico 404', dynamic(['127.0.0.256', '192.168.1.1'])),
		improperlyDelimited = has_any_ipv4('05:04:54127.0.0.1 GET /favicon.ico 404', '127.0.0.1', '192.168.1.1')`)
	want := []bool{true, true, false, false}
	names := []string{"multiScalar", "dynArray", "invalidIP", "improperlyDelimited"}
	for i, w := range want {
		if got := result.Rows[0][i].(bool); got != w {
			t.Errorf("%s = %v, want %v", names[i], got, w)
		}
	}
}

// --- has_ipv4_prefix / has_any_ipv4_prefix ---

func TestHasIPv4PrefixWorkedExamples(t *testing.T) {
	result := queryResult(t, `print
		match = has_ipv4_prefix('05:04:54 127.0.0.1 GET /favicon.ico 404', '127.0.'),
		anyMatch = has_any_ipv4_prefix('05:04:54 127.0.0.1 GET /favicon.ico 404', '127.0.', '192.168.'),
		anyArrayMatch = has_any_ipv4_prefix('05:04:54 127.0.0.1 GET /favicon.ico 404', dynamic(["127.0.", "192.168."]))`)
	for i, name := range []string{"match", "anyMatch", "anyArrayMatch"} {
		if got := result.Rows[0][i].(bool); got != true {
			t.Errorf("%s = %v, want true", name, got)
		}
	}
}

// --- ipv4_is_in_any_range ---

func TestIPv4IsInAnyRangeWorkedExample(t *testing.T) {
	result := queryResult(t, `print Result=ipv4_is_in_any_range('192.168.1.6', '192.168.1.1/24', '10.0.0.1/8', '127.1.0.1/16')`)
	if got := result.Rows[0][0].(bool); got != true {
		t.Errorf("ipv4_is_in_any_range = %v, want true", got)
	}
}

// --- ipv4_is_match ---

func TestIPv4IsMatchWorkedExamples(t *testing.T) {
	result := queryResult(t, `datatable(ip1_string:string, ip2_string:string)
		[
		 "192.168.1.0",    "192.168.1.0",
		 "192.168.1.1/24", "192.168.1.255",
		 "192.168.1.1",    "192.168.1.255/24",
		 "192.168.1.1/30", "192.168.1.255/24",
		]
		| extend result = ipv4_is_match(ip1_string, ip2_string)`)
	if len(result.Rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(result.Rows))
	}
	for i, row := range result.Rows {
		if got := row[2].(bool); got != true {
			t.Errorf("row %d ipv4_is_match = %v, want true", i, got)
		}
	}
}

func TestIPv4IsMatchWithExplicitPrefixWorkedExamples(t *testing.T) {
	result := queryResult(t, `datatable(ip1_string:string, ip2_string:string, prefix:long)
		[
		 "192.168.1.1",    "192.168.1.0",   31,
		 "192.168.1.1/24", "192.168.1.255", 31,
		 "192.168.1.1",    "192.168.1.255", 24,
		]
		| extend result = ipv4_is_match(ip1_string, ip2_string, prefix)`)
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
	for i, row := range result.Rows {
		if got := row[3].(bool); got != true {
			t.Errorf("row %d ipv4_is_match(...,prefix) = %v, want true", i, got)
		}
	}
}

// --- ipv4_range_to_cidr_list ---

func TestIPv4RangeToCIDRListWorkedExample(t *testing.T) {
	result := queryResult(t, `print start_IP="1.1.128.0", end_IP="1.1.140.255"
		| project ipv4_range_list = ipv4_range_to_cidr_list(start_IP, end_IP)`)
	got := result.Rows[0][0].(string)
	want := `["1.1.128.0/21","1.1.136.0/22","1.1.140.0/24"]`
	if got != want {
		t.Errorf("ipv4_range_to_cidr_list = %v, want %v", got, want)
	}
}

// --- ipv4_netmask_suffix ---

func TestIPv4NetmaskSuffixWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(ip_string:string) ["10.1.2.3","192.168.1.1/24","127.0.0.1/16"]
		| extend cidr_suffix = ipv4_netmask_suffix(ip_string)`)
	want := []int64{32, 24, 16}
	for i, w := range want {
		if got := result.Rows[i][1].(int64); got != w {
			t.Errorf("row %d ipv4_netmask_suffix = %v, want %v", i, got, w)
		}
	}
}

// --- format_ipv4_mask, and the format_ipv4 embedded-prefix bug regression ---

func TestFormatIPv4AndFormatIPv4MaskWorkedExample(t *testing.T) {
	// Real bug: format_ipv4 previously couldn't parse an address
	// argument carrying its own embedded "/prefix" at all (row 3
	// below returned null), and neither format_ipv4 nor the new
	// format_ipv4_mask combined an embedded prefix with an explicit
	// one via MIN (row 3's explicit mask=32 must still yield the /24
	// network, not /32, because the embedded prefix is more
	// restrictive).
	result := queryResult(t, `datatable(address:string, mask:long)
		[
		 "192.168.1.1", 24,
		 "192.168.1.1", 32,
		 "192.168.1.1/24", 32,
		 "192.168.1.1/24", long(-1),
		]
		| extend result = format_ipv4(address, mask), result_mask = format_ipv4_mask(address, mask)`)
	if len(result.Rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(result.Rows))
	}
	wantResult := []string{"192.168.1.0", "192.168.1.1", "192.168.1.0", ""}
	wantMask := []string{"192.168.1.0/24", "192.168.1.1/32", "192.168.1.0/24", ""}
	for i := range result.Rows {
		gotResult, _ := result.Rows[i][2].(string)
		gotMask, _ := result.Rows[i][3].(string)
		if gotResult != wantResult[i] {
			t.Errorf("row %d format_ipv4 = %q, want %q", i, gotResult, wantResult[i])
		}
		if gotMask != wantMask[i] {
			t.Errorf("row %d format_ipv4_mask = %q, want %q", i, gotMask, wantMask[i])
		}
	}
}

// --- parse_ipv4_mask ---

func TestParseIPv4MaskBasic(t *testing.T) {
	result := queryResult(t, `print r=parse_ipv4_mask("192.168.1.1", 24)`)
	got := result.Rows[0][0].(int64)
	// 192.168.1.0 masked to /24, network order
	want := int64(192)<<24 | int64(168)<<16 | int64(1)<<8 | int64(0)
	if got != want {
		t.Errorf("parse_ipv4_mask = %v, want %v", got, want)
	}
}

// --- ipv4_compare embedded-prefix bug regression ---

func TestIPv4CompareEmbeddedPrefixWorkedExamples(t *testing.T) {
	// Real bug: ipv4_compare called parseIPv4ToUint32 (net.ParseIP)
	// directly on the raw argument, which fails outright on an
	// embedded "/prefix" suffix, so 3 of these 4 documented rows
	// previously returned null instead of the documented 0.
	result := queryResult(t, `datatable(ip1_string:string, ip2_string:string)
		[
		 "192.168.1.0",    "192.168.1.0",
		 "192.168.1.1/24", "192.168.1.255",
		 "192.168.1.1",    "192.168.1.255/24",
		 "192.168.1.1/30", "192.168.1.255/24",
		]
		| extend result = ipv4_compare(ip1_string, ip2_string)`)
	if len(result.Rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(result.Rows))
	}
	for i, row := range result.Rows {
		if got := row[2].(int64); got != 0 {
			t.Errorf("row %d ipv4_compare = %v, want 0", i, got)
		}
	}
}

// --- ipv4_is_private embedded-prefix bug regression ---

func TestIPv4IsPrivateEmbeddedPrefixWorkedExample(t *testing.T) {
	// Real bug: ipv4_is_private called net.ParseIP directly, which
	// fails on an embedded "/prefix" suffix, so
	// ipv4_is_private('192.168.1.1/24') returned null instead of the
	// documented true.
	result := queryResult(t, `datatable(ip_string:string) ["10.1.2.3","192.168.1.1/24","127.0.0.1"]
		| extend result = ipv4_is_private(ip_string)`)
	want := []bool{true, true, false}
	for i, w := range want {
		if got := result.Rows[i][1].(bool); got != w {
			t.Errorf("row %d ipv4_is_private = %v, want %v", i, got, w)
		}
	}
}

// --- ipv4_is_in_range equal-IPs (no range notation) bug regression ---

func TestIPv4IsInRangeEqualIPsWorkedExample(t *testing.T) {
	// Real bug: ipv4_is_in_range called net.ParseCIDR directly on the
	// range argument, which requires a "/N" suffix, so the documented
	// "equal IPs, no range notation" case (implicit /32) returned
	// null instead of true.
	result := queryResult(t, `datatable(ip_address:string, ip_range:string)
		[
		 "192.168.1.1", "192.168.1.1",
		 "192.168.1.1", "192.168.1.255/24",
		]
		| extend result = ipv4_is_in_range(ip_address, ip_range)`)
	want := []bool{true, true}
	for i, w := range want {
		if got := result.Rows[i][2].(bool); got != w {
			t.Errorf("row %d ipv4_is_in_range = %v, want %v", i, got, w)
		}
	}
}

