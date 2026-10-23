package engine

import "testing"

// func_hash_xxhash_test.go — hash()/hash_xxhash64(), added 2026-08-18.
// Every numeric expectation below is real ADX's own documented worked
// example value, verified bit-for-bit, not just "produces a hash."
// hash_combine()/hash_many() are deliberately NOT implemented -- see
// scalar_function_backlog.md's own scope note: their combine
// algorithm isn't in any public source (the Kusto-Query-Language
// GitHub repo is parser/semantic-analyzer only, not the execution
// engine), and five standard candidate combine algorithms (boost-
// style, FNV-style, XOR, addition, rehash-of-concatenated-bytes) were
// tried against the real worked example and none matched.

func TestHashStringWorkedExample(t *testing.T) {
	result := queryResult(t, `print result=hash("World")`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 1846988464401551951 {
		t.Errorf("hash(\"World\") = %v (%T), want int64(1846988464401551951)", result.Rows[0][0], result.Rows[0][0])
	}
}

func TestHashStringWithModWorkedExample(t *testing.T) {
	result := queryResult(t, `print result=hash("World", 100)`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 51 {
		t.Errorf("hash(\"World\", 100) = %v (%T), want int64(51)", result.Rows[0][0], result.Rows[0][0])
	}
}

// TestHashDatetimeWorkedExample is the critical case: real ADX's own
// documented value does NOT match hashing any textual form of the
// datetime, nor this engine's own internal nanoseconds-since-Unix-
// epoch representation -- it only matches the little-endian 8-byte
// encoding of the value converted to real ADX's own internal .NET
// "ticks" representation (100ns since 0001-01-01, not 1970-01-01).
// This was independently derived and confirmed against this exact
// value before being implemented, not assumed.
func TestHashDatetimeWorkedExample(t *testing.T) {
	result := queryResult(t, `print result=hash(datetime("2015-01-01"))`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 1380966698541616202 {
		t.Errorf("hash(datetime(\"2015-01-01\")) = %v (%T), want int64(1380966698541616202)", result.Rows[0][0], result.Rows[0][0])
	}
}

// TestHashHelloWorkedExample guards the second string value used in
// real ADX's own hash_combine() worked example (hash("Hello") ==
// 753694413698530628) -- hash_combine itself isn't implemented (see
// this file's own top comment), but the underlying hash() value it
// depends on is independently verified here.
func TestHashHelloWorkedExample(t *testing.T) {
	result := queryResult(t, `print result=hash("Hello")`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 753694413698530628 {
		t.Errorf("hash(\"Hello\") = %v (%T), want int64(753694413698530628)", result.Rows[0][0], result.Rows[0][0])
	}
}

// TestHashXxhash64AliasMatchesHash guards that hash_xxhash64() (the
// algorithm-pinned name) produces the identical result to hash()
// (the "may change" name) for the same input, since both currently
// document xxhash64 as the algorithm.
func TestHashXxhash64AliasMatchesHash(t *testing.T) {
	result := queryResult(t, `print a=hash("World"), b=hash_xxhash64("World")`)
	if result.Rows[0][0] != result.Rows[0][1] {
		t.Errorf("hash = %v, hash_xxhash64 = %v, want equal", result.Rows[0][0], result.Rows[0][1])
	}
}

// TestHashNullAndDynamic: a null hashes to ADX's null/empty constant;
// a dynamic argument is rejected, as ADX does.
func TestHashNullAndDynamic(t *testing.T) {
	result := queryResult(t, `print r = hash(long(null))`)
	expectCell(t, result, 0, 0, "8393271554155345163")
	queryError(t, `print r = hash(dynamic([1]))`)
	queryError(t, `print r = hash(parse_json("{}"))`)
}

// TestHashModZeroOrNegativeIgnored guards the undocumented-but-
// reasonable edge case where mod <= 0: rather than dividing by zero
// or producing a negative-range result contradicting the documented
// "[0, mod-1]" contract, the unmodded hash is returned.
func TestHashModZeroOrNegativeIgnored(t *testing.T) {
	result := queryResult(t, `print a=hash("World", 0), b=hash("World", -5), c=hash("World")`)
	if result.Rows[0][0] != result.Rows[0][2] {
		t.Errorf("hash(\"World\", 0) = %v, want same as hash(\"World\") = %v", result.Rows[0][0], result.Rows[0][2])
	}
	if result.Rows[0][1] != result.Rows[0][2] {
		t.Errorf("hash(\"World\", -5) = %v, want same as hash(\"World\") = %v", result.Rows[0][1], result.Rows[0][2])
	}
}

// TestHashLongIsDeterministic guards the INFERRED (not worked-
// example-verified) long/int handling: same value hashes the same
// way every time, and different values hash differently. This is a
// consistency guard, not a bit-exact-match-to-real-ADX guard -- no
// worked example for a numeric hash() input was found, so exactness
// against real ADX isn't claimed for this type (see this file's own
// top comment and the hash case's own doc comment in func_net.go).
func TestHashLongIsDeterministic(t *testing.T) {
	result := queryResult(t, `print a=hash(42), b=hash(42), c=hash(43)`)
	if result.Rows[0][0] != result.Rows[0][1] {
		t.Errorf("hash(42) not deterministic: %v vs %v", result.Rows[0][0], result.Rows[0][1])
	}
	if result.Rows[0][0] == result.Rows[0][2] {
		t.Errorf("hash(42) and hash(43) collided: both %v", result.Rows[0][0])
	}
}
