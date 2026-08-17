package engine

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalNetFunc handles IP, URL, encoding, and hashing functions.
func evalNetFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "parse_ipv4":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_ipv4 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		ip := net.ParseIP(fmt.Sprintf("%v", val))
		if ip == nil {
			return nil, true, nil
		}
		ip4 := ip.To4()
		if ip4 == nil {
			return nil, true, nil
		}
		return int64(ip4[0])<<24 | int64(ip4[1])<<16 | int64(ip4[2])<<8 | int64(ip4[3]), true, nil

	case "ipv4_is_private":
		// Real bug found and fixed 2026-08-17, while verifying the
		// IPv4 family against real ADX docs: this case called
		// net.ParseIP directly on the raw argument, which fails
		// outright on an embedded "/prefix" suffix — so
		// ipv4_is_private('192.168.1.1/24') returned null instead of
		// the documented true (real ADX's own docs explicitly state
		// "IPv4 strings can be masked using IP-prefix notation" and
		// give ipv4_is_private('192.168.1.1/24') == true as a worked
		// example). Reproduced live before fixing. Fixed by stripping
		// an embedded prefix via parseIPv4WithPrefix (the prefix
		// itself doesn't change which private range the base address
		// falls in, so only the address half is needed here).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("ipv4_is_private requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		n, _, ok := parseIPv4WithPrefix(fmt.Sprintf("%v", val))
		if !ok {
			return nil, true, nil
		}
		ip := net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
			_, network, _ := net.ParseCIDR(cidr)
			if network.Contains(ip) {
				return true, true, nil
			}
		}
		return false, true, nil

	case "ipv4_is_in_range":
		// Real bug found and fixed 2026-08-17: this case called
		// net.ParseCIDR directly on the range argument, which
		// requires a "/N" suffix to be present at all — so the
		// documented "equal IPs, no range notation" worked example
		// (ipv4_is_in_range('192.168.1.1', '192.168.1.1') == true,
		// treating a bare IP as an implicit /32) returned null
		// instead of true. Reproduced live before fixing. Fixed by
		// routing both arguments through parseIPv4WithPrefix (which
		// already defaults a missing "/N" to /32) and comparing
		// masked addresses directly, the same pattern as
		// ipv4_is_match/ipv4_compare above, rather than requiring
		// net.ParseCIDR's stricter syntax.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("ipv4_is_in_range requires 2 arguments")
		}
		ipVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		cidrVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil || cidrVal == nil {
			return nil, true, nil
		}
		ipN, _, ipOk := parseIPv4WithPrefix(fmt.Sprintf("%v", ipVal))
		rangeN, rangePrefix, rangeOk := parseIPv4WithPrefix(fmt.Sprintf("%v", cidrVal))
		if !ipOk || !rangeOk {
			return nil, true, nil
		}
		mask := uint32(0xFFFFFFFF) << uint(32-rangePrefix)
		return (ipN & mask) == (rangeN & mask), true, nil

	case "hash_sha256":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("hash_sha256 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		h := sha256.Sum256([]byte(fmt.Sprintf("%v", val)))
		return hex.EncodeToString(h[:]), true, nil

	case "base64_encode_tostring":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("base64_encode_tostring requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%v", val))), true, nil

	case "base64_decode_tostring":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("base64_decode_tostring requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		decoded, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", val))
		if err != nil {
			return nil, true, nil
		}
		return string(decoded), true, nil

	case "url_encode_component":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("url_encode_component requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return strings.ReplaceAll(
			strings.ReplaceAll(
				strings.ReplaceAll(
					strings.ReplaceAll(
						strings.ReplaceAll(fmt.Sprintf("%v", val), "%", "%25"),
						" ", "%20"),
					"&", "%26"),
				"=", "%3D"),
			"+", "%2B"), true, nil

	case "url_encode":
		// Same character set as url_encode_component immediately
		// above (matching that sibling function's own deliberately
		// narrow, hand-rolled 5-character encoder rather than
		// introducing a different, more "complete" but inconsistent
		// approach between the two), verified against real ADX's own
		// docs before implementing: url_encode "differs from
		// url_encode_component by encoding spaces as '+' and not as
		// ' '" (application/x-www-form-urlencoded style) — the only
		// real difference between the two functions.
		//
		// Order matters here in a way it doesn't for
		// url_encode_component: a literal '+' in the INPUT must be
		// escaped to %2B before space is turned into '+' below, or an
		// original '+' would become indistinguishable from an encoded
		// space once both share the same output character.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("url_encode requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return strings.ReplaceAll(
			strings.ReplaceAll(
				strings.ReplaceAll(
					strings.ReplaceAll(
						strings.ReplaceAll(fmt.Sprintf("%v", val), "%", "%25"),
						"+", "%2B"),
					" ", "+"),
				"&", "%26"),
			"=", "%3D"), true, nil

	case "parse_url":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_url requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		result := parseURLToMap(fmt.Sprintf("%v", val))
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "parse_urlquery":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_urlquery requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", val)
		if strings.HasPrefix(s, "?") {
			s = s[1:]
		}
		params := make(map[string]string)
		for _, pair := range strings.Split(s, "&") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 {
				params[kv[0]] = kv[1]
			} else if len(kv) == 1 && kv[0] != "" {
				params[kv[0]] = ""
			}
		}
		b, _ := json.Marshal(params)
		return string(b), true, nil

	case "parse_path":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_path requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", val)
		s = strings.ReplaceAll(s, "\\", "/")
		lastSlash := strings.LastIndex(s, "/")
		dir := ""
		filename := s
		if lastSlash >= 0 {
			dir = s[:lastSlash]
			filename = s[lastSlash+1:]
		}
		ext := ""
		dotIdx := strings.LastIndex(filename, ".")
		if dotIdx >= 0 {
			ext = filename[dotIdx:]
		}
		nameOnly := filename
		if dotIdx >= 0 {
			nameOnly = filename[:dotIdx]
		}
		result := map[string]string{
			"DirectoryName":       dir,
			"FileName":            filename,
			"Extension":           ext,
			"FileNameNoExtension": nameOnly,
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "parse_command_line":
		// parse_command_line(command_line, parser_type) — added
		// 2026-08-17. Only parser_type="windows" is documented as
		// supported by real ADX; implements the standard Win32
		// CommandLineToArgvW tokenization algorithm (whitespace-
		// delimited arguments, double-quote-bounded arguments allow
		// embedded whitespace, an even run of N backslashes before a
		// quote emits N/2 literal backslashes and the quote toggles
		// quoting, an odd run emits (N-1)/2 backslashes plus one
		// literal escaped quote character) — verified exactly against
		// real ADX's own worked example:
		// parse_command_line('echo "hello world!"', 'windows') ==
		// ["echo","hello world!"]. A parser_type other than "windows"
		// returns null rather than guessing at undocumented behavior,
		// since real ADX's own docs state windows is the only
		// currently-supported value without describing what happens
		// otherwise.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("parse_command_line requires 2 arguments")
		}
		cmdVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		typeVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if cmdVal == nil || typeVal == nil {
			return nil, true, nil
		}
		if fmt.Sprintf("%v", typeVal) != "windows" {
			return nil, true, nil
		}
		args := parseWindowsCommandLine(fmt.Sprintf("%v", cmdVal))
		b, _ := json.Marshal(args)
		return string(b), true, nil

	default:
		return nil, false, nil
	}
}

// parseWindowsCommandLine implements the standard Win32
// CommandLineToArgvW tokenization algorithm, verified against real
// ADX's own parse_command_line worked example
// ('echo "hello world!"' -> ["echo","hello world!"]) — see the
// parse_command_line case above for the full rule description.
func parseWindowsCommandLine(s string) []string {
	var args []string
	runes := []rune(s)
	n := len(runes)
	i := 0
	skipWS := func() {
		for i < n && (runes[i] == ' ' || runes[i] == '\t') {
			i++
		}
	}
	skipWS()
	for i < n {
		var cur []rune
		inQuotes := false
		for i < n {
			c := runes[i]
			switch {
			case c == '\\':
				count := 0
				for i < n && runes[i] == '\\' {
					count++
					i++
				}
				if i < n && runes[i] == '"' {
					for k := 0; k < count/2; k++ {
						cur = append(cur, '\\')
					}
					if count%2 == 1 {
						cur = append(cur, '"')
						i++
					} else {
						inQuotes = !inQuotes
						i++
					}
				} else {
					for k := 0; k < count; k++ {
						cur = append(cur, '\\')
					}
				}
			case c == '"':
				inQuotes = !inQuotes
				i++
			case !inQuotes && (c == ' ' || c == '\t'):
				goto endArg
			default:
				cur = append(cur, c)
				i++
			}
		}
	endArg:
		args = append(args, string(cur))
		skipWS()
	}
	return args
}

// evalNetFuncExtended handles additional network and identity functions.
func evalNetFuncExtended(fc *parser.FuncCall, schema *types.Schema, row types.Row) (interface{}, bool, error) {
	switch fc.Name {
	case "has_ipv4":
		// has_ipv4(text, ip_or_prefix) — checks if an IPv4 address appears in text
		//
		// Real bug found and fixed 2026-08-17: this case's own comment
		// claimed "word boundary aware" but the implementation only
		// did a plain strings.Contains with no boundary check at all,
		// so an IP directly glued to preceding digits (the exact
		// "improperly delimited" case real ADX's own has_ipv4 docs
		// explicitly test and require to return false — e.g.
		// has_ipv4('05:04:54127.0.0.1 GET...', '127.0.0.1')) silently
		// returned true here. Reproduced live before fixing (see
		// TestHasIPv4RequiresProperDelimiting). Fixed by routing
		// through the new shared hasIPv4Delimited helper (added
		// 2026-08-17 alongside has_any_ipv4/has_ipv4_prefix/
		// has_any_ipv4_prefix below), which requires the character
		// immediately before and after the match, if present, to be
		// non-alphanumeric — matching real ADX's own documented rule
		// exactly. The pre-existing CIDR/'/'-prefix branch below is
		// preserved as-is: it predates this fix, is not part of real
		// ADX's own has_ipv4 signature (which only ever takes a plain
		// IP address, not a range — has_ipv4_prefix is the
		// real function for that), and is left untouched rather than
		// removed, since removing it is a separate, larger behavior
		// change not scoped to this delimiting fix.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("has_ipv4 requires 2 arguments")
		}
		textVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ipVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil || ipVal == nil {
			return false, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		ipStr := fmt.Sprintf("%v", ipVal)
		// If ip_or_prefix contains '/', match the subnet
		if strings.Contains(ipStr, "/") {
			// Check if any IP in text falls in range
			words := strings.Fields(text)
			for _, w := range words {
				w = strings.Trim(w, ",.;:()[]\"'")
				if net.ParseIP(w) != nil {
					if ipInRange(w, ipStr) {
						return true, true, nil
					}
				}
			}
			return false, true, nil
		}
		return hasIPv4Delimited(text, ipStr), true, nil

	case "has_any_ipv4":
		// has_any_ipv4(source, ip_address [, ip_address_2, ...]) or
		// has_any_ipv4(source, dynamic([...])) — added 2026-08-17.
		// Verified against real ADX's own has_any_ipv4 docs and all
		// four of its worked examples (multi-scalar true, dynamic-
		// array true, invalid-IP false, improperly-delimited false).
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("has_any_ipv4 requires at least 2 arguments")
		}
		textVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return false, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		ips, err := collectIPv4SearchTerms(fc.Args[1:], schema, row)
		if err != nil {
			return nil, true, err
		}
		for _, ipStr := range ips {
			if hasIPv4Delimited(text, ipStr) {
				return true, true, nil
			}
		}
		return false, true, nil

	case "has_ipv4_prefix":
		// has_ipv4_prefix(source, ip_address_prefix) — added
		// 2026-08-17. Verified against real ADX's own docs: a valid
		// prefix is either a complete IPv4 address or a prefix ending
		// with a dot (192., 192.168., 192.168.1.); IP occurrences must
		// still be properly delimited by non-alphanumeric characters.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("has_ipv4_prefix requires 2 arguments")
		}
		textVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		prefixVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil || prefixVal == nil {
			return false, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		prefix := fmt.Sprintf("%v", prefixVal)
		return hasIPv4PrefixDelimited(text, prefix), true, nil

	case "has_any_ipv4_prefix":
		// has_any_ipv4_prefix(source, prefix [, prefix2, ...]) or
		// has_any_ipv4_prefix(source, dynamic([...])) — added
		// 2026-08-17, same variadic/array calling convention as
		// has_any_ipv4 above, verified against real ADX's own
		// has_any_ipv4_prefix worked example
		// (has_any_ipv4_prefix('...127.0.0.1...', '127.0.', '192.168.') == true).
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("has_any_ipv4_prefix requires at least 2 arguments")
		}
		textVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return false, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		prefixes, err := collectIPv4SearchTerms(fc.Args[1:], schema, row)
		if err != nil {
			return nil, true, err
		}
		for _, p := range prefixes {
			if hasIPv4PrefixDelimited(text, p) {
				return true, true, nil
			}
		}
		return false, true, nil

	case "ipv4_is_in_any_range":
		// ipv4_is_in_any_range(Ipv4Address, Ipv4Range [, Ipv4Range...])
		// or ipv4_is_in_any_range(Ipv4Address, dynamic([...])) — added
		// 2026-08-17. Verified against real ADX's own worked example:
		// ipv4_is_in_any_range('192.168.1.6', '192.168.1.1/24',
		// '10.0.0.1/8', '127.1.0.1/16') == true. Returns null (not
		// false) if the address itself fails to parse, matching real
		// ADX's own documented null case for conversion failure.
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("ipv4_is_in_any_range requires at least 2 arguments")
		}
		ipVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil {
			return nil, true, nil
		}
		ipStr := fmt.Sprintf("%v", ipVal)
		if net.ParseIP(ipStr) == nil {
			return nil, true, nil
		}
		ranges, err := collectIPv4SearchTerms(fc.Args[1:], schema, row)
		if err != nil {
			return nil, true, err
		}
		for _, r := range ranges {
			if ipInRange(ipStr, r) {
				return true, true, nil
			}
		}
		return false, true, nil

	case "ipv4_is_match":
		// ipv4_is_match(ip1, ip2 [, prefix]) — added 2026-08-17.
		// Verified against real ADX's own docs and all seven rows of
		// its two worked-example tables: the effective comparison
		// mask is the MINIMUM of ip1's own embedded prefix (default
		// 32), ip2's own embedded prefix (default 32), and the
		// optional explicit prefix argument (default 32) — confirmed
		// by hand-checking every row (e.g. ip1='192.168.1.1/30',
		// ip2='192.168.1.255/24' -> min(30,24)=24 -> same /24 network
		// -> true; ip1='192.168.1.1/24', ip2='192.168.1.255',
		// prefix=31 -> min(24,32,31)=24 -> true).
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("ipv4_is_match requires 2-3 arguments")
		}
		ip1Val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ip2Val, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ip1Val == nil || ip2Val == nil {
			return nil, true, nil
		}
		n1, p1, ok1 := parseIPv4WithPrefix(fmt.Sprintf("%v", ip1Val))
		n2, p2, ok2 := parseIPv4WithPrefix(fmt.Sprintf("%v", ip2Val))
		if !ok1 || !ok2 {
			return nil, true, nil
		}
		effective := p1
		if p2 < effective {
			effective = p2
		}
		if len(fc.Args) == 3 {
			pVal, err := evalExpr(fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			if p, ok := toInt64(pVal); ok && int(p) < effective {
				effective = int(p)
			}
		}
		mask := uint32(0xFFFFFFFF) << uint(32-effective)
		return (n1 & mask) == (n2 & mask), true, nil

	case "ipv4_range_to_cidr_list":
		// ipv4_range_to_cidr_list(StartAddress, EndAddress) — added
		// 2026-08-17. Standard largest-fitting-aligned-block CIDR-
		// splitting algorithm, verified exactly against real ADX's
		// own worked example: ipv4_range_to_cidr_list("1.1.128.0",
		// "1.1.140.255") == ["1.1.128.0/21", "1.1.136.0/22",
		// "1.1.140.0/24"] — hand-traced all three blocks against the
		// algorithm before trusting it (see
		// TestIPv4RangeToCIDRListWorkedExample).
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("ipv4_range_to_cidr_list requires 2 arguments")
		}
		startVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		endVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if startVal == nil || endVal == nil {
			return nil, true, nil
		}
		startN := parseIPv4ToUint32(fmt.Sprintf("%v", startVal))
		endN := parseIPv4ToUint32(fmt.Sprintf("%v", endVal))
		if startN == nil || endN == nil {
			return nil, true, nil
		}
		list := ipv4RangeToCIDRList(*startN, *endN)
		b, _ := json.Marshal(list)
		return string(b), true, nil

	case "ipv4_netmask_suffix":
		// ipv4_netmask_suffix(ip) — added 2026-08-17. Verified against
		// real ADX's own worked example: no embedded suffix returns
		// 32 (full netmask, not null/0); an embedded suffix returns
		// that value directly (10.1.2.3->32, 192.168.1.1/24->24,
		// 127.0.0.1/16->16). null only on genuine parse failure.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("ipv4_netmask_suffix requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		_, prefix, ok := parseIPv4WithPrefix(fmt.Sprintf("%v", val))
		if !ok {
			return nil, true, nil
		}
		return int64(prefix), true, nil

	case "format_ipv4_mask":
		// format_ipv4_mask(ip [, prefix]) — added 2026-08-17. Verified
		// exactly against real ADX's own 4-row worked example table:
		// ('192.168.1.1',24)->"192.168.1.0/24",
		// ('192.168.1.1',32)->"192.168.1.1/32",
		// ('192.168.1.1/24',32)->"192.168.1.0/24" (the effective mask
		// is the MINIMUM of the address's own embedded prefix and the
		// explicit prefix argument -- 24 here, NOT 32 -- confirmed by
		// this exact row: an explicit prefix arg does NOT simply
		// override an embedded one, matching the same "most
		// restrictive wins" rule already used by ipv4_is_match, and
		// the same fix applied to format_ipv4 above after this row
		// caught format_ipv4's own real, pre-existing bug), and
		// ('192.168.1.1/24',-1)->"" (empty string, NOT null, on
		// conversion failure — an out-of-range prefix argument is a
		// real conversion failure per the docs, not a null-propagating
		// case, confirmed by this exact worked-example row).
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("format_ipv4_mask requires 1-2 arguments")
		}
		ipVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil {
			return "", true, nil
		}
		n, embeddedPrefix, ok := parseIPv4WithPrefix(fmt.Sprintf("%v", ipVal))
		if !ok {
			return "", true, nil
		}
		effective := embeddedPrefix
		if len(fc.Args) == 2 {
			pVal, err := evalExpr(fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			p, pok := toInt64(pVal)
			if !pok || p < 0 || p > 32 {
				return "", true, nil
			}
			if int(p) < effective {
				effective = int(p)
			}
		}
		mask := uint32(0xFFFFFFFF) << uint(32-effective)
		masked := n & mask
		return fmt.Sprintf("%d.%d.%d.%d/%d", (masked>>24)&0xFF, (masked>>16)&0xFF, (masked>>8)&0xFF, masked&0xFF, effective), true, nil

	case "parse_ipv4_mask":
		// parse_ipv4_mask(ip, prefix) — added 2026-08-17. Verified
		// against real ADX's own docs: converts ip+prefix to a masked
		// long, same numeric convention as the already-implemented
		// parse_ipv4/format_ipv4 (network-order 32-bit value packed
		// into an int64), required (not optional) prefix argument.
		// Uses parseIPv4WithPrefix + min-of-both-prefixes for
		// consistency with the real, confirmed behavior of
		// format_ipv4/format_ipv4_mask above (an embedded "/N" in the
		// address argument combines with the explicit prefix via
		// MIN, not override) — no worked example with an embedded
		// prefix was found in the real docs for this specific
		// function to confirm against directly, so this is applying
		// the verified sibling-function pattern rather than a
		// separately-confirmed worked example; flagged here rather
		// than silently assumed.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("parse_ipv4_mask requires 2 arguments")
		}
		ipVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		prefixVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil || prefixVal == nil {
			return nil, true, nil
		}
		n, embeddedPrefix, ok := parseIPv4WithPrefix(fmt.Sprintf("%v", ipVal))
		if !ok {
			return nil, true, nil
		}
		p, pok := toInt64(prefixVal)
		if !pok || p < 0 || p > 32 {
			return nil, true, nil
		}
		effective := embeddedPrefix
		if int(p) < effective {
			effective = int(p)
		}
		mask := uint32(0xFFFFFFFF) << uint(32-effective)
		return int64(n & mask), true, nil

	case "ipv4_compare":
		// ipv4_compare(ip1, ip2 [, prefix]) — returns -1, 0, or 1
		//
		// Real bug found and fixed 2026-08-17, discovered while
		// verifying ipv4_is_match/format_ipv4 against real ADX's own
		// docs: this case used parseIPv4ToUint32 directly on the raw
		// ip1Str/ip2Str, which calls net.ParseIP and fails outright on
		// a string carrying an embedded "/prefix" suffix (e.g.
		// "192.168.1.1/24") — so any embedded-prefix argument made
		// this whole function return null, even though real ADX's own
		// ipv4_compare docs' very first worked example uses embedded
		// prefixes on 3 of 4 rows and expects 0 (equal), not null.
		// It also ignored any embedded prefix even when parsing
		// happened to succeed, using only the explicit prefix argument
		// (default 32) — real ADX's own docs confirm the effective
		// mask is the MINIMUM of both IPs' own embedded prefixes and
		// the explicit prefix argument, the identical rule already
		// correctly implemented for ipv4_is_match above. Reproduced
		// live (ipv4_compare("192.168.1.1/24","192.168.1.255") gave
		// null instead of the documented 0) before fixing. Fixed by
		// routing through the same parseIPv4WithPrefix +
		// min-of-all-three-prefixes logic as ipv4_is_match.
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("ipv4_compare requires 2-3 arguments")
		}
		ip1Val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ip2Val, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ip1Val == nil || ip2Val == nil {
			return nil, true, nil
		}
		n1, p1, ok1 := parseIPv4WithPrefix(fmt.Sprintf("%v", ip1Val))
		n2, p2, ok2 := parseIPv4WithPrefix(fmt.Sprintf("%v", ip2Val))
		if !ok1 || !ok2 {
			return nil, true, nil
		}
		effective := p1
		if p2 < effective {
			effective = p2
		}
		if len(fc.Args) == 3 {
			pVal, err := evalExpr(fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			if p, ok := toInt64(pVal); ok && int(p) < effective {
				effective = int(p)
			}
		}

		mask := uint32(0xFFFFFFFF) << uint(32-effective)
		m1 := n1 & mask
		m2 := n2 & mask
		switch {
		case m1 < m2:
			return int64(-1), true, nil
		case m1 > m2:
			return int64(1), true, nil
		default:
			return int64(0), true, nil
		}

	case "format_ipv4":
		// format_ipv4(ip [, prefix]) — formats IP with optional prefix mask
		//
		// Real bug found and fixed 2026-08-17, while verifying the
		// new format_ipv4_mask against real ADX's own 4-row worked
		// example table (format-ipv4-mask-function docs, which
		// documents format_ipv4 and format_ipv4_mask side by side
		// from the same datatable): this case never parsed an
		// embedded "/prefix" out of the address argument itself, only
		// out of the separate prefix parameter — so
		// format_ipv4('192.168.1.1/24', 32) returned null (via
		// parseIPv4ToUint32 -> net.ParseIP failing outright on the
		// literal "/24" suffix) instead of the documented
		// "192.168.1.0" (the real, effective mask is the MINIMUM of
		// the address's own embedded prefix and the explicit prefix
		// argument — 24 here, not 32 — the same "most restrictive
		// wins" rule already correctly used by ipv4_is_match above).
		// Reproduced live against the exact docs row before fixing
		// (see TestFormatIPv4EmbeddedPrefixWorkedExample). Fixed by
		// routing through the same parseIPv4WithPrefix +
		// min-of-both-prefixes logic as the new format_ipv4_mask
		// case below, and switching the parse-failure return from
		// null to "" (empty string) for a non-null but unparseable
		// address — matching real ADX's own documented "conversion
		// isn't successful -> empty string" contract exactly (a null
		// *argument* still propagates as null, unchanged).
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("format_ipv4 requires 1-2 arguments")
		}
		ipVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil {
			return nil, true, nil
		}
		n, embeddedPrefix, ok := parseIPv4WithPrefix(fmt.Sprintf("%v", ipVal))
		if !ok {
			return "", true, nil
		}
		effective := embeddedPrefix
		if len(fc.Args) == 2 {
			pVal, err := evalExpr(fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			p, pok := toInt64(pVal)
			if !pok || p < 0 || p > 32 {
				return "", true, nil
			}
			if int(p) < effective {
				effective = int(p)
			}
		}
		mask := uint32(0xFFFFFFFF) << uint(32-effective)
		masked := n & mask
		return uint32ToIPv4String(masked), true, nil

	case "hash_md5":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("hash_md5 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		h := md5.Sum([]byte(fmt.Sprintf("%v", val)))
		return fmt.Sprintf("%x", h), true, nil

	case "hash_sha1":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("hash_sha1 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		h := sha1.Sum([]byte(fmt.Sprintf("%v", val)))
		return fmt.Sprintf("%x", h), true, nil

	case "new_guid":
		// Generate a v4 UUID
		b := make([]byte, 16)
		rand.Read(b)
		b[6] = (b[6] & 0x0f) | 0x40 // version 4
		b[8] = (b[8] & 0x3f) | 0x80 // variant 10
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), true, nil

	case "rand":
		// rand() or rand(N) — random float or random int
		if len(fc.Args) == 0 {
			return rand.Float64(), true, nil
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if n, ok := toInt64(val); ok && n > 0 {
			return int64(rand.Intn(int(n))), true, nil
		}
		return rand.Float64(), true, nil

	default:
		return nil, false, nil
	}
}

// parseIPv4ToUint32 parses an IPv4 string to a uint32.
func parseIPv4ToUint32(s string) *uint32 {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	ip = ip.To4()
	if ip == nil {
		return nil
	}
	n := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	return &n
}

// parseURLToMap parses a URL string into a map of components.
func parseURLToMap(s string) map[string]string {
	result := map[string]string{"Scheme": "", "Host": "", "Port": "", "Path": "", "Query": "", "Fragment": ""}
	if idx := strings.Index(s, "#"); idx >= 0 {
		result["Fragment"] = s[idx+1:]
		s = s[:idx]
	}
	if idx := strings.Index(s, "?"); idx >= 0 {
		result["Query"] = s[idx+1:]
		s = s[:idx]
	}
	if idx := strings.Index(s, "://"); idx >= 0 {
		result["Scheme"] = s[:idx]
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		result["Path"] = s[idx:]
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		result["Port"] = s[idx+1:]
		s = s[:idx]
	}
	result["Host"] = s
	return result
}

// ipInRange checks if an IP address falls within a CIDR range.
func ipInRange(ipStr, cidrStr string) bool {
	ip := net.ParseIP(ipStr)
	_, network, err := net.ParseCIDR(cidrStr)
	if err != nil || ip == nil {
		return false
	}
	return network.Contains(ip)
}

// toInt64 converts a value to int64, returning false if not numeric.
func toInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	default:
		return 0, false
	}
}

// --- IPv4 text-search / range helpers (added 2026-08-17) ---

// isAlnumByte reports whether b is an ASCII letter or digit — the
// exact "alphanumeric" test real ADX's own has_ipv4/has_any_ipv4/
// has_ipv4_prefix/has_any_ipv4_prefix docs require for proper
// delimiting.
func isAlnumByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ipv4TextOccurrences returns every starting index at which needle
// appears as a substring of text (including overlapping occurrences).
func ipv4TextOccurrences(text, needle string) []int {
	var idxs []int
	if needle == "" {
		return idxs
	}
	start := 0
	for {
		i := strings.Index(text[start:], needle)
		if i < 0 {
			break
		}
		idx := start + i
		idxs = append(idxs, idx)
		start = idx + 1
	}
	return idxs
}

// ipv4ProperlyDelimited reports whether the occurrence of length
// bytes starting at idx in text has non-alphanumeric characters (or
// a string boundary) immediately before and after it — real ADX's
// own documented delimiting rule for has_ipv4 and its siblings.
func ipv4ProperlyDelimited(text string, idx, length int) bool {
	if idx > 0 && isAlnumByte(text[idx-1]) {
		return false
	}
	end := idx + length
	if end < len(text) && isAlnumByte(text[end]) {
		return false
	}
	return true
}

// hasIPv4Delimited implements the shared logic behind has_ipv4 and
// has_any_ipv4: ipStr must itself be a valid IPv4 address (an invalid
// address never matches, per real ADX's own documented example), and
// at least one of its occurrences in text must be properly delimited.
func hasIPv4Delimited(text, ipStr string) bool {
	if net.ParseIP(ipStr) == nil || strings.Contains(ipStr, ":") {
		return false
	}
	for _, idx := range ipv4TextOccurrences(text, ipStr) {
		if ipv4ProperlyDelimited(text, idx, len(ipStr)) {
			return true
		}
	}
	return false
}

// isValidIPv4Prefix checks real ADX's own documented definition of a
// valid IPv4 address prefix: either a complete IPv4 address (no
// trailing dot), or 1-3 dot-separated octet groups each in 0-255
// followed by a trailing dot (e.g. "192.", "192.168.", "192.168.1.").
func isValidIPv4Prefix(s string) bool {
	if s == "" {
		return false
	}
	if !strings.HasSuffix(s, ".") {
		return net.ParseIP(s) != nil && !strings.Contains(s, ":")
	}
	trimmed := strings.TrimSuffix(s, ".")
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// hasIPv4PrefixDelimited implements the shared logic behind
// has_ipv4_prefix and has_any_ipv4_prefix: prefix must be a real
// ADX-valid IPv4 address prefix (see isValidIPv4Prefix), and at
// least one of its occurrences in text must be properly delimited on
// its left edge (there is no fixed-length right edge to check for a
// partial prefix, unlike a complete address match).
func hasIPv4PrefixDelimited(text, prefix string) bool {
	if !isValidIPv4Prefix(prefix) {
		return false
	}
	for _, idx := range ipv4TextOccurrences(text, prefix) {
		if idx > 0 && isAlnumByte(text[idx-1]) {
			continue
		}
		return true
	}
	return false
}

// collectIPv4SearchTerms implements the shared variadic-or-dynamic-
// array calling convention used by has_any_ipv4, has_any_ipv4_prefix,
// and ipv4_is_in_any_range: real ADX accepts either several separate
// scalar string arguments, or a single dynamic array argument, for
// the "list of terms to search for" parameter — verified against
// each function's own worked examples using both forms.
func collectIPv4SearchTerms(argExprs []parser.Expr, schema *types.Schema, row types.Row) ([]string, error) {
	var terms []string
	for _, argExpr := range argExprs {
		val, err := evalExpr(argExpr, schema, row)
		if err != nil {
			return nil, err
		}
		if val == nil {
			continue
		}
		if arr, ok := parseJSONArray(val); ok && len(argExprs) == 1 {
			for _, el := range arr {
				terms = append(terms, fmt.Sprintf("%v", el))
			}
			return terms, nil
		}
		terms = append(terms, fmt.Sprintf("%v", val))
	}
	return terms, nil
}

// parseIPv4WithPrefix parses an IPv4 string that may carry an
// embedded IP-prefix-notation suffix (e.g. "192.168.1.1/24"),
// returning the address as a uint32, its prefix length (defaulting
// to 32 when no "/N" suffix is present, per real ADX's own documented
// ipv4_netmask_suffix behavior), and whether parsing succeeded.
func parseIPv4WithPrefix(s string) (uint32, int, bool) {
	addr := s
	prefix := 32
	if idx := strings.Index(s, "/"); idx >= 0 {
		addr = s[:idx]
		p, err := strconv.Atoi(s[idx+1:])
		if err != nil || p < 0 || p > 32 {
			return 0, 0, false
		}
		prefix = p
	}
	n := parseIPv4ToUint32(addr)
	if n == nil {
		return 0, 0, false
	}
	return *n, prefix, true
}

// ipv4RangeToCIDRList converts an inclusive [start, end] IPv4 range
// into the minimal list of aligned CIDR blocks that exactly covers
// it — the standard largest-fitting-aligned-block algorithm, verified
// by hand-tracing against real ADX's own worked example
// (1.1.128.0-1.1.140.255 -> ["1.1.128.0/21", "1.1.136.0/22",
// "1.1.140.0/24"]) before trusting it. Uses uint64 internally so the
// final block's end-of-range arithmetic can't silently wrap a uint32.
func ipv4RangeToCIDRList(startAddr, endAddr uint32) []string {
	var out []string
	start := uint64(startAddr)
	end := uint64(endAddr)
	if start > end {
		return out
	}
	for start <= end {
		// tz: how many low-order bits of start are zero (its own
		// alignment) — a block can be at most this large and still
		// start exactly at `start`.
		tz := 32
		if start != 0 {
			tz = 0
			for tz < 32 && (start>>uint(tz))&1 == 0 {
				tz++
			}
		}
		remaining := end - start + 1
		maxBits := 0
		for maxBits < 32 && (uint64(1)<<uint(maxBits+1)) <= remaining {
			maxBits++
		}
		blockBits := tz
		if maxBits < blockBits {
			blockBits = maxBits
		}
		prefix := 32 - blockBits
		blockSize := uint64(1) << uint(blockBits)
		out = append(out, fmt.Sprintf("%s/%d", uint32ToIPv4String(uint32(start)), prefix))
		start += blockSize
	}
	return out
}

// uint32ToIPv4String formats a uint32 as a dotted-quad IPv4 string.
func uint32ToIPv4String(n uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", (n>>24)&0xFF, (n>>16)&0xFF, (n>>8)&0xFF, n&0xFF)
}
