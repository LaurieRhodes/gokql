package engine

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"

	"github.com/LaurieRhodes/gokql/pkg/engine/uaparser"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// dotnetTicksAtUnixEpoch is the number of .NET "ticks" (100-nanosecond
// intervals since 0001-01-01T00:00:00) at the Unix epoch
// (1970-01-01T00:00:00) -- the well-known .NET constant
// DateTime.UnixEpoch.Ticks. Used only by hash()/hash_xxhash64() to
// convert this engine's own internal datetime representation
// (nanoseconds since the UNIX epoch) into the representation real
// ADX actually hashes (see the hash() case's own doc comment for the
// full verification story: this constant was independently derived
// and confirmed to reproduce real ADX's own documented worked example
// exactly, not copied on faith from a single source).
const dotnetTicksAtUnixEpoch = 621355968000000000

// guidBytesLEToString converts 16 raw bytes in .NET's own internal
// Guid byte layout (the form base64_decode_toguid's own base64
// payload is actually encoded in) into the standard display-order
// guid string (8-4-4-4-12). .NET's Guid struct stores Data1 (4
// bytes) and Data2/Data3 (2 bytes each) in LITTLE-ENDIAN order
// internally, while Data4 (the trailing 8 bytes) is stored as-is —
// verified exactly against base64_encode_fromguid's own real worked
// example (guidStringToBytesLE's own doc comment has the full
// verification story, including a real discrepancy found in
// base64_decode_toguid's own separate doc page).
func guidBytesLEToString(b []byte) string {
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[3], b[2], b[1], b[0],
		b[5], b[4],
		b[7], b[6],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15])
}

// guidStringToBytesLE is guidBytesLEToString's own exact inverse —
// converts a standard display-order guid string into .NET's internal
// mixed-endian 16-byte layout. Verified exactly against real ADX's
// own base64_encode_fromguid worked example:
// base64_encode_fromguid(toguid("ae3133f2-6e22-49ae-b06a-16e6a9b212eb"))
// == "8jMxriJurkmwahbmqbIS6w==" — computed independently (byte-level
// arithmetic, not copied from any single source) and confirmed to
// reproduce that exact base64 string.
//
// A genuine discrepancy was found while cross-checking this same
// algorithm against base64_decode_toguid's own SEPARATE worked
// example on that function's own doc page: it states
// base64_decode_toguid("JpbpECu8dUy7Pv5gbeJXAA==") produces a guid
// whose third hex group is "754c", but decoding that exact base64
// string through this algorithm produces "4c75" instead — a
// two-character transposition. Since this algorithm is independently
// confirmed self-consistent (round-trips exactly) against the SECOND
// worked example above, and a simple transposition typo is the most
// plausible explanation for the first page's own mismatch (rather
// than genuinely different behavior between the two functions, which
// are documented as exact inverses of one another), the standard,
// internally-consistent algorithm is implemented here rather than a
// one-off variant that would break the round trip with
// base64_encode_fromguid.
func guidStringToBytesLE(guidStr string) ([]byte, bool) {
	hex32 := strings.ReplaceAll(guidStr, "-", "")
	if len(hex32) != 32 {
		return nil, false
	}
	raw, err := hex.DecodeString(hex32)
	if err != nil {
		return nil, false
	}
	out := make([]byte, 16)
	out[0], out[1], out[2], out[3] = raw[3], raw[2], raw[1], raw[0]
	out[4], out[5] = raw[5], raw[4]
	out[6], out[7] = raw[7], raw[6]
	copy(out[8:16], raw[8:16])
	return out, true
}

// evalNetFunc handles IP, URL, encoding, and hashing functions.
func evalNetFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "parse_ipv4":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_ipv4 requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		cidrVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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

	case "base64_decode_toarray":
		// base64_decode_toarray(base64_string) — verified against
		// real ADX's own docs before implementing, its own worked
		// example checked exactly: base64_decode_toarray("S3VzdG8=")
		// == [75,117,115,116,111] (the raw decoded bytes of "Kusto",
		// each byte as its own array element, unsigned 0-255, "an
		// array of long values" per the docs' own wording). Invalid
		// base64 input: the docs' own worked example for this case is
		// genuinely ambiguous between null and empty string (a
		// malformed, non-4-aligned base64 string is fed through this
		// function and the doc's own prose separately says the
		// DOWNSTREAM encode-back call "will return null" for the
		// resulting empty array, but the actual output table for that
		// same chain shows a blank cell, which could mean either) —
		// resolved here in favor of null, matching this codebase's
		// own established convention for every other decode/parse
		// function on invalid input (base64_decode_tostring included,
		// immediately above).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("base64_decode_toarray requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		arr := make([]int64, len(decoded))
		for i, b := range decoded {
			arr[i] = int64(b)
		}
		bts, _ := json.Marshal(arr)
		return string(bts), true, nil

	case "base64_encode_fromarray":
		// base64_encode_fromarray(byte_array) — the documented
		// inverse of base64_decode_toarray(), verified against real
		// ADX's own worked example (round-tripping
		// base64_decode_toarray("S3VzdG8=") back through this
		// function reproduces "S3VzdG8=" exactly). Each array element
		// is truncated to its low 8 bits (a byte) — real ADX's own
		// docs state "byte is an integer type" for this parameter,
		// consistent with the array holding long values 0-255 from
		// base64_decode_toarray's own output.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("base64_encode_fromarray requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		arr, ok := parseJSONArray(val)
		if !ok {
			return nil, true, nil
		}
		bts := make([]byte, len(arr))
		for i, el := range arr {
			bts[i] = byte(int64(types.ToFloat64(el)))
		}
		return base64.StdEncoding.EncodeToString(bts), true, nil

	case "base64_decode_toguid":
		// base64_decode_toguid(base64_string) — verified against real
		// ADX's own docs, INCLUDING independently cross-checking its
		// own worked example against the separate, independently
		// verified base64_encode_fromguid() worked example (see that
		// case's own comment below): the raw base64-decoded bytes are
		// NOT the guid's own display-order bytes -- they're in
		// .NET's own internal mixed-endian Guid layout (Data1: 4
		// bytes, little-endian; Data2: 2 bytes, little-endian; Data3:
		// 2 bytes, little-endian; Data4: 8 bytes, as-is) --
		// implemented via guidBytesLEToString below. A GENUINE
		// DISCREPANCY was found in this function's own doc page: its
		// worked example states base64_decode_toguid(
		// "JpbpECu8dUy7Pv5gbeJXAA==") == "...-754c-..." for the third
		// hex group, but decoding that exact base64 string through
		// the standard .NET mixed-endian algorithm produces "...-
		// 4c75-..." instead -- and that SAME algorithm, applied to
		// base64_encode_fromguid's own separate worked example, round
		// -trips EXACTLY (guid "...-49ae-..." <-> base64
		// "8jMxriJurkmwahbmqbIS6w==", confirmed both directions).
		// Since the algorithm is directly confirmed self-consistent
		// (matches in both directions) against the SECOND worked
		// example, and a plausible two-character transposition typo
		// ("754c" vs "4c75") is the simplest explanation for the
		// FIRST example's own mismatch, the standard algorithm is
		// implemented here rather than a one-off inconsistent variant
		// that would break the round trip with base64_encode_fromguid.
		// Invalid input (wrong length after decoding, or invalid
		// base64) returns null, matching the docs' own second example
		// (`base64_decode_toguid("abcd1231")` -> null).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("base64_decode_toguid requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		decoded, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", val))
		if err != nil || len(decoded) != 16 {
			return nil, true, nil
		}
		return guidBytesLEToString(decoded), true, nil

	case "base64_encode_fromguid":
		// base64_encode_fromguid(guid) — the documented inverse of
		// base64_decode_toguid(), verified exactly against real ADX's
		// own worked example: base64_encode_fromguid(toguid(
		// "ae3133f2-6e22-49ae-b06a-16e6a9b212eb")) ==
		// "8jMxriJurkmwahbmqbIS6w==" -- reproduced bit-for-bit via
		// guidStringToBytesLE below, the exact inverse of
		// guidBytesLEToString used by base64_decode_toguid above (see
		// that case's own comment for the cross-verification story
		// between the two functions' worked examples). Real ADX's own
		// docs state passing a non-guid value "will [have] an error
		// thrown" -- this engine returns null instead for an
		// unparseable guid argument, the established convention this
		// codebase uses for invalid input everywhere else, rather
		// than introducing static per-argument type-checking (a
		// substantial, unrelated architecture change) for this one
		// function alone.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("base64_encode_fromguid requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		le, ok := guidStringToBytesLE(fmt.Sprintf("%v", val))
		if !ok {
			return nil, true, nil
		}
		return base64.StdEncoding.EncodeToString(le), true, nil

	case "gzip_compress_to_base64_string":
		// gzip_compress_to_base64_string(value) — verified against
		// real ADX's own docs before implementing: standard gzip
		// compression, base64-encoded. No worked example with exact
		// bytes given (compressed output isn't byte-stable across
		// implementations/compression levels), so this is verified by
		// ROUND TRIP against gzip_decompress_from_base64_string
		// (immediately below) rather than a fixed expected string —
		// the standard `compress/gzip` package produces a real,
		// valid, standards-compliant gzip stream, which is what the
		// docs' own contract actually requires (interoperability with
		// any standard gzip reader, not byte-identical output to a
		// specific reference implementation).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("gzip_compress_to_base64_string requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, werr := gw.Write([]byte(fmt.Sprintf("%v", val))); werr != nil {
			gw.Close()
			return "", true, nil
		}
		if cerr := gw.Close(); cerr != nil {
			return "", true, nil
		}
		return base64.StdEncoding.EncodeToString(buf.Bytes()), true, nil

	case "gzip_decompress_from_base64_string":
		// gzip_decompress_from_base64_string(base64_string) —
		// verified against real ADX's own docs: decodes base64, then
		// gzip-decompresses. Real ADX's own docs state this returns
		// an EMPTY string (not null) on any failure ("returns an
		// empty result if decompression or decoding failed") — a
		// genuinely different contract from most of this codebase's
		// other decode functions (which return null), followed
		// exactly here rather than the more common null convention.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("gzip_decompress_from_base64_string requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return "", true, nil
		}
		decoded, derr := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", val))
		if derr != nil {
			return "", true, nil
		}
		gr, grerr := gzip.NewReader(bytes.NewReader(decoded))
		if grerr != nil {
			return "", true, nil
		}
		defer gr.Close()
		out, rerr := io.ReadAll(gr)
		if rerr != nil {
			return "", true, nil
		}
		return string(out), true, nil

	case "zlib_compress_to_base64_string":
		// zlib_compress_to_base64_string(value) — verified against
		// real ADX's own docs: standard zlib compression (window size
		// 15, the only size real ADX documents as supported — Go's
		// own compress/zlib always uses a 32KB window internally,
		// matching window size 15 exactly, so no explicit parameter
		// is needed), base64-encoded. Verified by round trip against
		// zlib_decompress_from_base64_string, the same reasoning as
		// gzip_compress_to_base64_string above (compressed byte
		// output isn't stable across implementations; standards
		// compliance, not byte-identical output, is the real
		// contract).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("zlib_compress_to_base64_string requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, werr := zw.Write([]byte(fmt.Sprintf("%v", val))); werr != nil {
			zw.Close()
			return "", true, nil
		}
		if cerr := zw.Close(); cerr != nil {
			return "", true, nil
		}
		return base64.StdEncoding.EncodeToString(buf.Bytes()), true, nil

	case "zlib_decompress_from_base64_string":
		// zlib_decompress_from_base64_string(base64_string) —
		// verified against real ADX's own docs: decodes base64, then
		// zlib-decompresses. Same "returns empty (not null) on
		// failure" contract as the gzip decompress function above,
		// followed exactly.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("zlib_decompress_from_base64_string requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return "", true, nil
		}
		decoded, derr := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", val))
		if derr != nil {
			return "", true, nil
		}
		zr, zrerr := zlib.NewReader(bytes.NewReader(decoded))
		if zrerr != nil {
			return "", true, nil
		}
		defer zr.Close()
		out, rerr := io.ReadAll(zr)
		if rerr != nil {
			return "", true, nil
		}
		return string(out), true, nil

	case "url_encode_component":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("url_encode_component requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		cmdVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		typeVal, err := evalExpr(eng, fc.Args[1], schema, row)
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

	case "parse_user_agent":
		// parse_user_agent(user_agent_string, look_for) — added
		// 2026-08-18. Real ADX's own docs describe this as "built on
		// regex checks of the input string against a huge number of
		// predefined patterns" — this implementation uses that exact
		// pattern database (ua-parser/uap-core's own regexes.yaml,
		// embedded verbatim under pkg/engine/uaparser/, Apache-2.0
		// licensed, see that package's NOTICE.md), not a hand-rolled
		// approximation, via the pkg/engine/uaparser package which
		// implements uap-core's own published matching algorithm.
		// Verified exactly against two of real ADX's own worked
		// examples: a Nokia N81/Symbian/Series60 user agent (a
		// deliberately tricky case — the string contains a literal
		// "Safari/4" substring that a naive parser would misclassify,
		// but the correct answer, "Nokia OSS Browser" version 3.1
		// sourced from a completely different token in the string,
		// falls straight out of using the real pattern database) and
		// an AdobeAIR user agent.
		//
		// look_for is REQUIRED (not optional) per real ADX's own
		// docs, and may be a single string or a dynamic array; the
		// output only includes the requested categories (verified
		// directly: a single "browser" request returns only a
		// {"Browser": {...}} object, no OperatingSystem/Device keys
		// at all). Output field order (Family before MajorVersion
		// before MinorVersion before Patch, etc, and Family before
		// Brand before Model for Device — NOT alphabetical, which
		// would put Brand before Family) is preserved via ordered Go
		// structs rather than a map, to match real ADX's own worked
		// examples exactly.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("parse_user_agent requires 2 arguments")
		}
		uaVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		lookForVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if uaVal == nil || lookForVal == nil {
			return nil, true, nil
		}
		var targets []string
		if arr, ok := parseJSONArray(lookForVal); ok {
			for _, el := range arr {
				targets = append(targets, fmt.Sprintf("%v", el))
			}
		} else {
			targets = []string{fmt.Sprintf("%v", lookForVal)}
		}
		uaStr := fmt.Sprintf("%v", uaVal)
		result, err := parseUserAgentResult(uaStr, targets)
		if err != nil {
			return nil, true, err
		}
		b2, _ := json.Marshal(result)
		return string(b2), true, nil

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
func evalNetFuncExtended(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (interface{}, bool, error) {
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
		textVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ipVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		textVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return false, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		ips, err := collectIPv4SearchTerms(eng, fc.Args[1:], schema, row)
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
		textVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		prefixVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		textVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return false, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		prefixes, err := collectIPv4SearchTerms(eng, fc.Args[1:], schema, row)
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
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
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
		ranges, err := collectIPv4SearchTerms(eng, fc.Args[1:], schema, row)
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
		ip1Val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ip2Val, err := evalExpr(eng, fc.Args[1], schema, row)
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
			pVal, err := evalExpr(eng, fc.Args[2], schema, row)
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
		startVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		endVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
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
			pVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		prefixVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		ip1Val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ip2Val, err := evalExpr(eng, fc.Args[1], schema, row)
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
			pVal, err := evalExpr(eng, fc.Args[2], schema, row)
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
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
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
			pVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		h := sha1.Sum([]byte(fmt.Sprintf("%v", val)))
		return fmt.Sprintf("%x", h), true, nil

	case "hash", "hash_xxhash64":
		// hash(source [, mod]) / hash_xxhash64(source [, mod]) —
		// verified against real ADX's own docs before implementing:
		// both use the xxhash64 algorithm (real ADX's own hash()
		// docs state this explicitly, noting it "may change"; this
		// engine implements both names identically since that IS
		// what both currently document). Genuinely bit-exact verified
		// against real ADX's own worked examples, not just "produces
		// A hash":
		//   hash("World")                    == 1846988464401551951
		//   hash("World", 100)                == 51
		//   hash(datetime("2015-01-01"))      == 1380966698541616202
		// The string case matches xxhash64 of the raw UTF-8 bytes
		// directly (github.com/cespare/xxhash/v2, seed 0), confirmed
		// exactly. The datetime case does NOT match hashing any
		// textual form, or this engine's own internal nanoseconds-
		// since-Unix-epoch representation directly -- it only matches
		// the little-endian 8-byte encoding of the value converted to
		// REAL ADX'S OWN internal representation: .NET "ticks" (100ns
		// intervals since 0001-01-01, not 1970-01-01), independently
		// derived (dotnetTicksAtUnixEpoch, the well-known .NET
		// DateTime.UnixEpoch.Ticks constant) and confirmed to
		// reproduce the exact documented value bit-for-bit.
		//
		// long/int/real/timespan are handled by the same
		// little-endian-8-byte-of-the-underlying-value principle
		// (timespan needs no epoch conversion, since this engine's
		// own internal timespan ticks are already the same 100ns-tick
		// unit real ADX itself uses) -- this is a REASONABLE
		// INFERENCE from the one confirmed non-string case (datetime),
		// not independently worked-example-verified for long/int/real/
		// timespan specifically; no worked example for those types
		// was found. Every other type (bool, guid, dynamic) falls
		// back to hashing the value's own string form, the safest
		// defensible default absent any evidence either way -- NOT
		// claimed to match real ADX bit-for-bit for those types.
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("%s requires 1 or 2 arguments (source [, mod])", fc.Name)
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		argType := inferExprType(eng, fc.Args[0], schema)
		var data []byte
		switch argType {
		case types.TypeDatetime:
			ticks := types.ToInt64(val)/100 + dotnetTicksAtUnixEpoch
			data = make([]byte, 8)
			binary.LittleEndian.PutUint64(data, uint64(ticks))
		case types.TypeTimespan, types.TypeLong, types.TypeInt:
			data = make([]byte, 8)
			binary.LittleEndian.PutUint64(data, uint64(types.ToInt64(val)))
		case types.TypeReal:
			data = make([]byte, 8)
			binary.LittleEndian.PutUint64(data, math.Float64bits(types.ToFloat64(val)))
		default:
			data = []byte(fmt.Sprintf("%v", val))
		}
		h := xxhash.Sum64(data)
		if len(fc.Args) == 2 {
			modVal, err := evalExpr(eng, fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			mod := types.ToInt64(modVal)
			if mod > 0 {
				// Unsigned arithmetic throughout: h can exceed
				// math.MaxInt64 (becoming negative once cast), and
				// real ADX's own contract requires the result in
				// [0, mod-1] regardless -- verified directly against
				// hash("World", 100) == 51 (1846988464401551951 mod
				// 100 == 51).
				return int64(h % uint64(mod)), true, nil
			}
		}
		return int64(h), true, nil

	case "new_guid":
		// Generate a v4 UUID
		b := make([]byte, 16)
		rand.Read(b)
		b[6] = (b[6] & 0x0f) | 0x40 // version 4
		b[8] = (b[8] & 0x3f) | 0x80 // variant 10
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), true, nil

	case "toguid":
		// toguid(value) — verified against real ADX's own docs before
		// implementing, its own worked example checked exactly: "the
		// conversion process takes the first 32 characters of the
		// input, ignoring properly located hyphens, validates that
		// the characters are between 0-9 or a-f, and then converts
		// the string into a guid scalar. The rest of the string is
		// ignored." Confirmed by hand-tracing the doc's own second
		// row: input "0123456789ab-cdef-0123-456789abcdef" has
		// hyphens in NON-standard-guid positions (12-4-4-12, not the
		// standard 8-4-4-4-12), yet still converts successfully to
		// "01234567-89ab-cdef-0123-456789abcdef" -- meaning hyphens
		// are simply stripped wherever they occur, not validated for
		// position, despite "properly located" in the prose;
		// implemented that way (strip all hyphens, take first 32 hex
		// chars of what remains) since that's what the worked example
		// actually demonstrates. A non-hex character within the first
		// 32 (after stripping), or fewer than 32 hex characters
		// remaining at all, returns null (the doc's own third row:
		// "a string that is not a guid" contains spaces and non-hex
		// letters, correctly producing an empty/null result).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("toguid requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		stripped := strings.ReplaceAll(fmt.Sprintf("%v", val), "-", "")
		if len(stripped) < 32 {
			return nil, true, nil
		}
		hex32 := strings.ToLower(stripped[:32])
		for _, c := range hex32 {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return nil, true, nil
			}
		}
		return hex32[0:8] + "-" + hex32[8:12] + "-" + hex32[12:16] + "-" + hex32[16:20] + "-" + hex32[20:32], true, nil

	case "tohex":
		// tohex(value [, minLength]) — verified against real ADX's
		// own docs before implementing, all 5 of its own worked
		// examples checked with exact values:
		//   tohex(256)                 == "100"
		//   tohex(-256)                == "ffffffffffffff00"   (64-bit two's complement)
		//   tohex(toint(-256), 8)      == "ffffff00"            (32-bit two's complement -- an int argument, not long)
		//   tohex(256, 8)              == "00000100"            (zero-padded to minLength)
		//   tohex(256, 2)              == "100"                 (already exceeds minLength, so minLength is ignored)
		// The int-vs-long distinction for a NEGATIVE value's two's-
		// complement width is real and load-bearing (an int argument
		// gets 32-bit width, a long argument gets 64-bit width) --
		// resolved via inferExprType(fc.Args[0], schema), the same
		// mechanism already used elsewhere in this codebase for
		// exactly this kind of static-type-dependent behavior, not
		// from the runtime Go value alone (which can't tell a
		// converted int32 apart from an int64 once both are held as
		// this engine's own internal int64 representation). This is
		// also what surfaced a real, pre-existing bug in
		// inferExprType itself (eval.go): toint() was grouped with
		// tolong() and both returned TypeLong, erasing exactly the
		// distinction this function needs -- fixed alongside this,
		// see that fix's own comment.
		//
		// The type-width floor (8 hex digits for int, 16 for long)
		// applies ONLY to a NEGATIVE value -- confirmed directly by
		// tohex(256) itself, which has NO minLength argument and
		// still returns the unpadded "100", not "0000000000000100"
		// (a long's own 16-digit floor would produce if it applied
		// unconditionally). A positive value has no implicit minimum
		// width at all; an explicit minLength argument only ever
		// pads UP toward whichever floor (type-width floor for
		// negatives, zero for positives) is already larger.
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("tohex requires 1 or 2 arguments (value [, minLength])")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		n := types.ToInt64(val)
		isInt32 := inferExprType(eng, fc.Args[0], schema) == types.TypeInt
		var hexStr string
		if isInt32 {
			hexStr = fmt.Sprintf("%x", uint32(int32(n)))
		} else {
			hexStr = fmt.Sprintf("%x", uint64(n))
		}
		minWidth := 0
		if n < 0 {
			if isInt32 {
				minWidth = 8
			} else {
				minWidth = 16
			}
		}
		if len(fc.Args) == 2 {
			mlVal, err := evalExpr(eng, fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			ml := int(types.ToInt64(mlVal))
			if ml > 16 {
				ml = 16
			}
			if ml > minWidth {
				minWidth = ml
			}
		}
		for len(hexStr) < minWidth {
			hexStr = "0" + hexStr
		}
		return hexStr, true, nil

	case "gettype":
		// gettype(value) — verified against real ADX's own docs
		// before implementing, all 12 of its own documented
		// expression/result pairs checked exactly. Two genuinely
		// distinct branches, not one: for a value whose STATIC (KQL)
		// type isn't dynamic, the returned name is just that static
		// type's own name (gettype("a")=="string", gettype(111)==
		// "long", gettype(1==1)=="bool", gettype(now())=="datetime",
		// gettype(1s)=="timespan", gettype(123.45)=="real",
		// gettype(guid(...))=="guid" -- all match this engine's own
		// existing types.KQLType.String() naming exactly, confirmed
		// by direct comparison, not assumed). For a value whose
		// static type IS dynamic, gettype reveals the ACTUAL runtime
		// shape of the parsed JSON value instead -- an object is
		// "dictionary" (not "object" or "bag"), an array is "array",
		// a JSON null (or a dynamic holding nothing at all) is
		// "null", and a JSON number is "int" for a whole number
		// confirmed by the doc's own gettype(parse_json('1'))=="int"
		// example.
		//
		// One inference beyond the directly-confirmed example set:
		// the docs show "int" for a small whole number but never show
		// what a LARGE whole number (beyond int32 range) or a
		// fractional number returns via the dynamic path specifically
		// -- "real" for a fractional dynamic number and "long" for a
		// whole dynamic number outside int32 range are both
		// implemented as the natural generalization consistent with
		// KQL's own int/long/real widening conventions elsewhere in
		// this codebase, not independently worked-example-verified
		// for those two specific cases.
		//
		// Three real, pre-existing bugs found and fixed while
		// verifying this against the doc's own full 12-case table,
		// before this case was ever committed:
		//   - inferExprType (eval.go) had NO case at all for
		//     parse_json/todynamic, so any dynamic value produced by
		//     them fell through to the FuncCall switch's own
		//     TypeString default -- gettype(parse_json(...)) always
		//     reported "string" regardless of the parsed value's own
		//     actual shape, for every single dynamic-typed example in
		//     the doc's own table (int/dictionary/array all silently
		//     wrong).
		//   - inferExprType also had no case for toguid() (this
		//     session's own earlier addition in this same file),
		//     so gettype(toguid(...)) also fell through to
		//     TypeString instead of TypeGUID.
		//   - inferExprType grouped toint() with tolong(), both
		//     returning TypeLong -- a separate, real bug surfaced
		//     while verifying tohex() just above (not gettype), fixed
		//     in the same pass; see that fix's own comment.
		// All three found by actually running this engine's own
		// dynamic-branch logic against the doc's own worked example
		// and getting the wrong answer, not by inspection alone.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("gettype requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		argType := inferExprType(eng, fc.Args[0], schema)
		if argType != types.TypeDynamic {
			return argType.String(), true, nil
		}
		if val == nil {
			return "null", true, nil
		}
		// An empty-string dynamic value is real ADX's own documented
		// "null" case (gettype(parse_json(''))=="null") -- confirmed
		// distinct from the general "non-empty malformed JSON wraps
		// as a string" case just below, which is itself justified by
		// parse_json's own separate docs ("if json ... isn't a
		// properly formatted JSON string, the returned value ...
		// holds the original string value"), not guessed.
		strVal := fmt.Sprintf("%v", val)
		if strVal == "" {
			return "null", true, nil
		}
		var parsed interface{}
		if jsonErr := json.Unmarshal([]byte(strVal), &parsed); jsonErr != nil {
			return "string", true, nil
		}
		switch pv := parsed.(type) {
		case nil:
			return "null", true, nil
		case bool:
			return "bool", true, nil
		case string:
			return "string", true, nil
		case map[string]interface{}:
			return "dictionary", true, nil
		case []interface{}:
			return "array", true, nil
		case float64:
			if pv == math.Trunc(pv) && !math.IsInf(pv, 0) {
				if pv >= math.MinInt32 && pv <= math.MaxInt32 {
					return "int", true, nil
				}
				return "long", true, nil
			}
			return "real", true, nil
		default:
			return "string", true, nil
		}

	case "rand":
		// rand() or rand(N) — random float or random int
		if len(fc.Args) == 0 {
			return rand.Float64(), true, nil
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if n, ok := toInt64(val); ok && n > 0 {
			return int64(rand.Intn(int(n))), true, nil
		}
		return rand.Float64(), true, nil

	case "parse_ipv6":
		// parse_ipv6(ip) — added 2026-08-17. Converts an IPv6 or
		// IPv4-notation string to its canonical, fully-expanded IPv6
		// string representation. Equivalent to parse_ipv6_mask(ip,
		// 128) — the explicit-128 default never reduces below
		// whatever prefix (if any) is already embedded in ip itself.
		// See parseIPv6WithPrefix's own doc comment for the full,
		// worked-example-verified rules this depends on.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_ipv6 requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		b, effPrefix, ok := parseIPv6WithPrefix(fmt.Sprintf("%v", val))
		if !ok {
			return "", true, nil
		}
		return canonicalIPv6String(maskIPv6(b, effPrefix)), true, nil

	case "parse_ipv6_mask":
		// parse_ipv6_mask(ip, prefix) — added 2026-08-17. Converts an
		// IPv6/IPv4-notation string plus an explicit prefix to a
		// canonical, fully-expanded, masked IPv6 string. Verified
		// exactly against real ADX's own 7-row worked-example table,
		// including two genuinely surprising details found ONLY in
		// the table's numeric output, not stated anywhere in the
		// prose docs:
		//   1. An embedded prefix on an IPv4-representable address
		//      (plain dotted-decimal, or the exact "::a.b.c.d"
		//      spelling) combines into the 128-bit effective prefix
		//      via a +96 offset (confirmed: '192.168.255.255/24'
		//      explicit-124 produces the IDENTICAL output to
		//      unprefixed '192.168.255.255' explicit-120 — both
		//      effective at 120 = 96+24). A genuine IPv6-notation
		//      address's own embedded prefix has NO offset.
		//   2. The literal "::a.b.c.d" spelling (which Go's own
		//      net.ParseIP renders with NO ffff inserted, the
		//      deprecated all-zero "IPv4-compatible" form) is
		//      auto-canonicalized by real ADX to the IPv4-MAPPED form
		//      instead (ffff inserted) — confirmed directly:
		//      '::192.168.255.255' with no masking still produces
		//      "...:ffff:c0a8:ffff", not "...:0000:c0a8:ffff".
		// See parseIPv6WithPrefix's own doc comment for the full
		// verification detail and the one genuinely untested edge
		// case (a non-zero-prefix IPv6 address embedding a dotted-
		// quad tail, e.g. "fe80::192.168.1.1") deliberately left to
		// ordinary parsing rather than guessed at.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("parse_ipv6_mask requires 2 arguments")
		}
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		prefixVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil || prefixVal == nil {
			return nil, true, nil
		}
		b, embeddedEff, ok := parseIPv6WithPrefix(fmt.Sprintf("%v", ipVal))
		if !ok {
			return "", true, nil
		}
		explicitP, pok := toInt64(prefixVal)
		if !pok || explicitP < 0 || explicitP > 128 {
			return "", true, nil
		}
		effective := embeddedEff
		if int(explicitP) < effective {
			effective = int(explicitP)
		}
		return canonicalIPv6String(maskIPv6(b, effective)), true, nil

	case "ipv6_compare":
		// ipv6_compare(ip1, ip2 [, prefix]) — added 2026-08-17. Same
		// min-of-all-supplied-prefixes combination rule as
		// ipv4_compare, generalized to 128-bit addresses via
		// parseIPv6WithPrefix. An explicit prefix argument (unlike an
		// embedded one) is always a raw 0-128 value, never offset —
		// verified against real ADX's own worked-example tables,
		// where a prefix ARGUMENT combines identically regardless of
		// whether either address is in IPv4 or IPv6 notation.
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("ipv6_compare requires 2-3 arguments")
		}
		ip1Val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ip2Val, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ip1Val == nil || ip2Val == nil {
			return nil, true, nil
		}
		b1, p1, ok1 := parseIPv6WithPrefix(fmt.Sprintf("%v", ip1Val))
		b2, p2, ok2 := parseIPv6WithPrefix(fmt.Sprintf("%v", ip2Val))
		if !ok1 || !ok2 {
			return nil, true, nil
		}
		effective := p1
		if p2 < effective {
			effective = p2
		}
		if len(fc.Args) == 3 {
			pVal, err := evalExpr(eng, fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			if p, ok := toInt64(pVal); ok && int(p) < effective {
				effective = int(p)
			}
		}
		m1 := maskIPv6(b1, effective)
		m2 := maskIPv6(b2, effective)
		switch {
		case m1 == m2:
			return int64(0), true, nil
		default:
			for i := 0; i < 16; i++ {
				if m1[i] != m2[i] {
					if m1[i] < m2[i] {
						return int64(-1), true, nil
					}
					return int64(1), true, nil
				}
			}
			return int64(0), true, nil
		}

	case "ipv6_is_match":
		// ipv6_is_match(ip1, ip2 [, prefix]) — added 2026-08-17.
		// Verified exactly against real ADX's own full worked-example
		// tables (both the embedded-prefix-only table and the
		// embedded-plus-explicit-prefix table — every one of their 21
		// combined rows, including 4 genuinely mixed IPv4/IPv6-
		// notation rows, hand-traced against the +96-offset formula
		// and confirmed to produce the documented true result).
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("ipv6_is_match requires 2-3 arguments")
		}
		ip1Val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ip2Val, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ip1Val == nil || ip2Val == nil {
			return nil, true, nil
		}
		b1, p1, ok1 := parseIPv6WithPrefix(fmt.Sprintf("%v", ip1Val))
		b2, p2, ok2 := parseIPv6WithPrefix(fmt.Sprintf("%v", ip2Val))
		if !ok1 || !ok2 {
			return nil, true, nil
		}
		effective := p1
		if p2 < effective {
			effective = p2
		}
		if len(fc.Args) == 3 {
			pVal, err := evalExpr(eng, fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			if p, ok := toInt64(pVal); ok && int(p) < effective {
				effective = int(p)
			}
		}
		return maskIPv6(b1, effective) == maskIPv6(b2, effective), true, nil

	case "ipv6_is_in_range":
		// ipv6_is_in_range(Ipv6Address, Ipv6Range) — added
		// 2026-08-17. The range's own embedded prefix (default 128 —
		// an implicit exact match — when absent) is the sole mask;
		// the address argument's own bytes are used unmasked, the
		// same asymmetric convention already correct for
		// ipv4_is_in_range.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("ipv6_is_in_range requires 2 arguments")
		}
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		rangeVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil || rangeVal == nil {
			return nil, true, nil
		}
		ipB, _, ipOk := parseIPv6WithPrefix(fmt.Sprintf("%v", ipVal))
		rangeB, rangePrefix, rangeOk := parseIPv6WithPrefix(fmt.Sprintf("%v", rangeVal))
		if !ipOk || !rangeOk {
			return nil, true, nil
		}
		return maskIPv6(ipB, rangePrefix) == maskIPv6(rangeB, rangePrefix), true, nil

	case "ipv6_is_in_any_range":
		// ipv6_is_in_any_range(Ipv6Address, Ipv6Range [, Ipv6Range...])
		// or ipv6_is_in_any_range(Ipv6Address, dynamic([...])) — added
		// 2026-08-17. Same variadic/array calling convention as
		// ipv4_is_in_any_range, reusing the same generic
		// collectIPv4SearchTerms helper (its logic is plain string
		// collection, not IPv4-specific despite the name).
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("ipv6_is_in_any_range requires at least 2 arguments")
		}
		ipVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ipVal == nil {
			return nil, true, nil
		}
		ipB, _, ipOk := parseIPv6WithPrefix(fmt.Sprintf("%v", ipVal))
		if !ipOk {
			return nil, true, nil
		}
		ranges, err := collectIPv4SearchTerms(eng, fc.Args[1:], schema, row)
		if err != nil {
			return nil, true, err
		}
		for _, r := range ranges {
			rangeB, rangePrefix, rangeOk := parseIPv6WithPrefix(r)
			if !rangeOk {
				continue
			}
			if maskIPv6(ipB, rangePrefix) == maskIPv6(rangeB, rangePrefix) {
				return true, true, nil
			}
		}
		return false, true, nil

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
func collectIPv4SearchTerms(eng *Engine, argExprs []parser.Expr, schema *types.Schema, row types.Row) ([]string, error) {
	var terms []string
	for _, argExpr := range argExprs {
		val, err := evalExpr(eng, argExpr, schema, row)
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

// --- IPv6 helpers (added 2026-08-17) ---

// parseIPv6WithPrefix parses an IPv6 or IPv4-notation address string
// (optionally carrying an embedded "/prefix"), returning its 16-byte
// representation, its effective prefix length in 128-bit space
// (default 128 when no embedded prefix is present), and whether
// parsing succeeded.
//
// Real ADX's own docs establish two rules that appear nowhere in the
// prose text, only in parse_ipv6_mask's own 7-row worked-example
// table's actual output numbers — both verified by hand-tracing every
// row against real ADX's own published output before trusting them:
//
//  1. An embedded prefix on an address whose TEXT contains a literal
//     '.' character — a plain dotted-decimal string, or the exact
//     "::a.b.c.d" spelling — is interpreted in 0-32 IPv4 space and
//     combined into the 128-bit effective prefix via a +96 offset.
//     Confirmed: '192.168.255.255/24' combined with an explicit
//     netmask=124 argument produces the IDENTICAL output to
//     unprefixed '192.168.255.255' combined with explicit netmask=120
//     — both effective at 120 (=96+24), not 124. A genuine IPv6-
//     notation address's own embedded prefix has NO such offset,
//     confirmed two separate ways: 'fe80::.../120' combined with an
//     explicit netmask=124 produces effective 120 (the smaller of 120
//     and 124 directly, not 216); and — critically — this is a
//     SYNTACTIC decision, not a check on the parsed byte VALUE: a
//     real, reproduced bug was found and fixed here where
//     '0:0:0:0:0:ffff:c0a8:ac/60' (real ADX's own
//     ipv6_is_in_any_range worked example) is written entirely in
//     colon-hex-group notation with no dot anywhere, even though its
//     parsed VALUE happens to fall in the same ::ffff:0:0/96 range as
//     a genuine IPv4-mapped address — an earlier version of this
//     function that decided the offset by inspecting the parsed byte
//     pattern instead of the source text wrongly rejected /60 as an
//     out-of-range IPv4 prefix (60 > 32), when real ADX's own
//     documented result requires /60 to be used directly as a raw
//     128-bit prefix.
//
//  2. The literal "::a.b.c.d" spelling — which Go's own net.ParseIP
//     renders with NO ffff inserted (bytes 10-11 stay 0x00 0x00, the
//     deprecated all-zero "IPv4-compatible" form; confirmed directly
//     against Go's own behavior before writing this override) — is
//     auto-canonicalized by real ADX to the IPv4-MAPPED form instead
//     (ffff inserted at bytes 10-11). Confirmed directly:
//     '::192.168.255.255' with netmask=128 (i.e. no masking applied
//     at all) still produces canonical output
//     "0000:0000:0000:0000:0000:ffff:c0a8:ffff", not
//     "...:0000:c0a8:ffff" — the only way to get an ffff there with
//     zero masking is if it was inserted at parse time, not derived
//     from any bit operation.
//
// A genuinely untested edge case, deliberately NOT covered: a real
// IPv6 address with a non-zero prefix ahead of an embedded dotted-quad
// tail (e.g. "fe80::192.168.1.1") — no worked example exercises this,
// so it's left to Go's own ordinary net.ParseIP parsing (no ffff
// override applied) rather than guessed at either way.
func parseIPv6WithPrefix(s string) ([16]byte, int, bool) {
	var out [16]byte
	addr := s
	var explicitEmbedded *int
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		p, err := strconv.Atoi(s[idx+1:])
		if err != nil || p < 0 {
			return out, 0, false
		}
		explicitEmbedded = &p
		addr = s[:idx]
	}

	isIPv4Notation := !strings.Contains(addr, ":")
	// Exactly the "::a.b.c.d" spelling: starts with "::", nothing
	// else colon-shaped after it, and the remainder is dot-bearing —
	// deliberately narrow, see the doc comment above.
	isCompatSpelling := strings.HasPrefix(addr, "::") &&
		!strings.Contains(addr[2:], ":") && strings.Contains(addr[2:], ".")
	// Whether an embedded prefix gets the +96 IPv4-space offset is
	// decided SYNTACTICALLY (does the address text itself contain a
	// literal '.' character), NOT by inspecting the parsed byte
	// value — a real, reproduced discrepancy found while verifying
	// ipv6_is_in_range against real ADX's own worked example:
	// '0:0:0:0:0:ffff:c0a8:ac/60' is written entirely in colon-hex-
	// group notation (no dot anywhere) even though its VALUE happens
	// to fall in the same ::ffff:0:0/96 range as a genuine IPv4-
	// mapped address. A byte-value-based check wrongly rejected its
	// embedded /60 as an out-of-range IPv4 prefix (60 > 32); a plain
	// syntactic "does the text contain a dot" check correctly treats
	// /60 as a raw 128-bit prefix instead, matching the documented
	// true result.
	embeddedGetsOffset := strings.Contains(addr, ".")

	if isIPv4Notation {
		ip4Val := net.ParseIP(addr)
		if ip4Val == nil {
			return out, 0, false
		}
		ip4 := ip4Val.To4()
		if ip4 == nil {
			return out, 0, false
		}
		out[10], out[11] = 0xff, 0xff
		copy(out[12:], ip4)
	} else {
		ipVal := net.ParseIP(addr)
		if ipVal == nil {
			return out, 0, false
		}
		ip16 := ipVal.To16()
		if ip16 == nil {
			return out, 0, false
		}
		copy(out[:], ip16)
		if isCompatSpelling {
			allZeroPrefix := true
			for i := 0; i < 10; i++ {
				if out[i] != 0 {
					allZeroPrefix = false
					break
				}
			}
			if allZeroPrefix && out[10] == 0 && out[11] == 0 {
				out[10], out[11] = 0xff, 0xff
			}
		}
	}

	effective := 128
	if explicitEmbedded != nil {
		p := *explicitEmbedded
		if embeddedGetsOffset {
			if p > 32 {
				return out, 0, false
			}
			effective = 96 + p
		} else {
			if p > 128 {
				return out, 0, false
			}
			effective = p
		}
	}
	return out, effective, true
}

// maskIPv6 zeroes every bit past prefix in a 16-byte IPv6 address.
func maskIPv6(b [16]byte, prefix int) [16]byte {
	var out [16]byte
	if prefix >= 128 {
		return b
	}
	if prefix <= 0 {
		return out
	}
	fullBytes := prefix / 8
	remBits := prefix % 8
	copy(out[:fullBytes], b[:fullBytes])
	if remBits > 0 && fullBytes < 16 {
		mask := byte(0xFF << uint(8-remBits))
		out[fullBytes] = b[fullBytes] & mask
	}
	return out
}

// --- parse_user_agent output shaping (added 2026-08-18) ---
//
// These structs exist purely to control JSON key ORDER: real ADX's
// own worked examples show Family before MajorVersion before
// MinorVersion before Patch (etc), which is NOT alphabetical order
// (alphabetically Brand < Family < Model for the Device object, but
// the documented order is Family, Brand, Model). Go's encoding/json
// preserves struct field declaration order but sorts map keys
// alphabetically, so a plain map[string]interface{} would silently
// produce the wrong key order for Device specifically.

type uaBrowserOut struct {
	Family       string `json:"Family"`
	MajorVersion string `json:"MajorVersion"`
	MinorVersion string `json:"MinorVersion"`
	Patch        string `json:"Patch"`
}

type uaOSOut struct {
	Family       string `json:"Family"`
	MajorVersion string `json:"MajorVersion"`
	MinorVersion string `json:"MinorVersion"`
	Patch        string `json:"Patch"`
	PatchMinor   string `json:"PatchMinor"`
}

type uaDeviceOut struct {
	Family string `json:"Family"`
	Brand  string `json:"Brand"`
	Model  string `json:"Model"`
}

type uaParseOut struct {
	Browser         *uaBrowserOut `json:"Browser,omitempty"`
	OperatingSystem *uaOSOut      `json:"OperatingSystem,omitempty"`
	Device          *uaDeviceOut  `json:"Device,omitempty"`
}

// parseUserAgentResult builds the requested subset of {Browser,
// OperatingSystem, Device} for uaStr, using the real ua-parser
// pattern database (pkg/engine/uaparser). targets are matched against
// real ADX's own documented values "browser", "os", "device" —
// unrecognized values are silently ignored (no worked example was
// found describing behavior for an invalid look_for value, so this
// engine doesn't guess at an error condition for it). Output category
// order always follows the canonical Browser/OperatingSystem/Device
// order regardless of the order targets were requested in, matching
// every worked example found (none exercises a non-canonical request
// order to confirm otherwise either way).
func parseUserAgentResult(uaStr string, targets []string) (*uaParseOut, error) {
	want := make(map[string]bool, len(targets))
	for _, t := range targets {
		want[t] = true
	}
	out := &uaParseOut{}
	if want["browser"] {
		b, err := uaparser.ParseBrowser(uaStr)
		if err != nil {
			return nil, err
		}
		out.Browser = &uaBrowserOut{Family: b.Family, MajorVersion: b.MajorVersion, MinorVersion: b.MinorVersion, Patch: b.Patch}
	}
	if want["os"] {
		o, err := uaparser.ParseOS(uaStr)
		if err != nil {
			return nil, err
		}
		out.OperatingSystem = &uaOSOut{Family: o.Family, MajorVersion: o.MajorVersion, MinorVersion: o.MinorVersion, Patch: o.Patch, PatchMinor: o.PatchMinor}
	}
	if want["device"] {
		d, err := uaparser.ParseDevice(uaStr)
		if err != nil {
			return nil, err
		}
		out.Device = &uaDeviceOut{Family: d.Family, Brand: d.Brand, Model: d.Model}
	}
	return out, nil
}

// canonicalIPv6String formats a 16-byte address as real ADX's own
// documented canonical form: 8 groups of 4 lowercase hex digits,
// fully expanded, zero-padded, no "::" compression — verified
// directly against parse_ipv6_mask's own worked-example output
// strings (e.g. "fe80:0000:0000:0000:085d:e82c:9446:7994").
func canonicalIPv6String(b [16]byte) string {
	return fmt.Sprintf("%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
