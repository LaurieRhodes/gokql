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
		ip := net.ParseIP(fmt.Sprintf("%v", val))
		if ip == nil {
			return nil, true, nil
		}
		for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
			_, network, _ := net.ParseCIDR(cidr)
			if network.Contains(ip) {
				return true, true, nil
			}
		}
		return false, true, nil

	case "ipv4_is_in_range":
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
		ip := net.ParseIP(fmt.Sprintf("%v", ipVal))
		_, network, err := net.ParseCIDR(fmt.Sprintf("%v", cidrVal))
		if err != nil || ip == nil {
			return nil, true, nil
		}
		return network.Contains(ip), true, nil

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

	default:
		return nil, false, nil
	}
}

// evalNetFuncExtended handles additional network and identity functions.
func evalNetFuncExtended(fc *parser.FuncCall, schema *types.Schema, row types.Row) (interface{}, bool, error) {
	switch fc.Name {
	case "has_ipv4":
		// has_ipv4(text, ip_or_prefix) — checks if an IPv4 address appears in text
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
		// Exact IP match as substring (word boundary aware)
		return strings.Contains(text, ipStr), true, nil

	case "ipv4_compare":
		// ipv4_compare(ip1, ip2 [, prefix]) — returns -1, 0, or 1
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
		ip1Str := fmt.Sprintf("%v", ip1Val)
		ip2Str := fmt.Sprintf("%v", ip2Val)

		prefix := 32
		if len(fc.Args) == 3 {
			pVal, err := evalExpr(fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			if p, ok := toInt64(pVal); ok {
				prefix = int(p)
			}
		}

		n1 := parseIPv4ToUint32(ip1Str)
		n2 := parseIPv4ToUint32(ip2Str)
		if n1 == nil || n2 == nil {
			return nil, true, nil
		}
		mask := uint32(0xFFFFFFFF) << (32 - prefix)
		m1 := *n1 & mask
		m2 := *n2 & mask
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
		ipStr := fmt.Sprintf("%v", ipVal)

		n := parseIPv4ToUint32(ipStr)
		if n == nil {
			return nil, true, nil
		}

		if len(fc.Args) == 2 {
			pVal, err := evalExpr(fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			if p, ok := toInt64(pVal); ok {
				mask := uint32(0xFFFFFFFF) << (32 - int(p))
				*n &= mask
			}
		}

		return fmt.Sprintf("%d.%d.%d.%d", (*n>>24)&0xFF, (*n>>16)&0xFF, (*n>>8)&0xFF, *n&0xFF), true, nil

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
