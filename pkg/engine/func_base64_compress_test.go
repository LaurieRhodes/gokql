package engine

import "testing"

// func_base64_compress_test.go — base64_decode_toarray,
// base64_encode_fromarray, base64_decode_toguid,
// base64_encode_fromguid, gzip_compress_to_base64_string,
// gzip_decompress_from_base64_string, zlib_compress_to_base64_string,
// zlib_decompress_from_base64_string, added 2026-08-18. Every case
// below is taken directly from real ADX's own docs' worked examples.

// --- base64_decode_toarray / base64_encode_fromarray ---

func TestBase64DecodeToArrayWorkedExample(t *testing.T) {
	result := queryResult(t, `print Quine=base64_decode_toarray("S3VzdG8=")`)
	want := `[75,117,115,116,111]`
	if got := result.Rows[0][0]; got != want {
		t.Errorf("base64_decode_toarray(\"S3VzdG8=\") = %v, want %v", got, want)
	}
}

func TestBase64EncodeFromArrayRoundTrip(t *testing.T) {
	result := queryResult(t, `let bytes_array = toscalar(print base64_decode_toarray("S3VzdG8="));
		print decoded_base64_string = base64_encode_fromarray(bytes_array)`)
	if got := result.Rows[0][0]; got != "S3VzdG8=" {
		t.Errorf("round trip = %v, want \"S3VzdG8=\"", got)
	}
}

// --- base64_decode_toguid / base64_encode_fromguid ---

// TestBase64EncodeFromGuidWorkedExample guards the one worked example
// that is directly, unambiguously confirmed (base64_decode_toguid's
// own separate worked example has a suspected transposition typo --
// see guidStringToBytesLE's own doc comment, func_net.go, for the
// full cross-verification reasoning; this test deliberately does NOT
// assert against that page's own literal value).
func TestBase64EncodeFromGuidWorkedExample(t *testing.T) {
	result := queryResult(t, `print Quine = base64_encode_fromguid(toguid("ae3133f2-6e22-49ae-b06a-16e6a9b212eb"))`)
	want := "8jMxriJurkmwahbmqbIS6w=="
	if got := result.Rows[0][0]; got != want {
		t.Errorf("base64_encode_fromguid(...) = %v, want %v", got, want)
	}
}

// TestBase64GuidRoundTrip guards that decode(encode(guid)) reproduces
// the original guid exactly -- the strongest possible confirmation
// that guidBytesLEToString and guidStringToBytesLE are genuine
// inverses of one another, independent of either doc page's own
// literal worked-example text.
func TestBase64GuidRoundTrip(t *testing.T) {
	result := queryResult(t, `print r = base64_decode_toguid(base64_encode_fromguid(toguid("ae3133f2-6e22-49ae-b06a-16e6a9b212eb")))`)
	want := "ae3133f2-6e22-49ae-b06a-16e6a9b212eb"
	if got := result.Rows[0][0]; got != want {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

// TestBase64DecodeToGuidInvalidReturnsNull guards real ADX's own
// documented behavior for invalid input.
func TestBase64DecodeToGuidInvalidReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = base64_decode_toguid("abcd1231")`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// TestBase64EncodeFromGuidInvalidReturnsNull guards this engine's own
// deliberate simplification: real ADX documents a hard error for a
// non-guid argument, but this codebase's established convention
// (used everywhere else) is null on invalid input, not a runtime
// error -- see this case's own doc comment, func_net.go.
func TestBase64EncodeFromGuidInvalidReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = base64_encode_fromguid("abcd1231")`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// --- gzip ---

func TestGzipRoundTrip(t *testing.T) {
	result := queryResult(t, `print r = gzip_decompress_from_base64_string(gzip_compress_to_base64_string("Hello, Kusto! This is a test string for compression."))`)
	want := "Hello, Kusto! This is a test string for compression."
	if got := result.Rows[0][0]; got != want {
		t.Errorf("gzip round trip = %v, want %v", got, want)
	}
}

// TestGzipDecompressInvalidReturnsEmptyNotNull guards real ADX's own
// documented "returns an empty result" contract for gzip decompress
// failures -- a genuinely different convention from most of this
// codebase's other decode functions, which return null.
func TestGzipDecompressInvalidReturnsEmptyNotNull(t *testing.T) {
	result := queryResult(t, `print r = gzip_decompress_from_base64_string("not valid base64 or gzip at all")`)
	if result.Rows[0][0] != "" {
		t.Errorf("r = %v (%T), want empty string, not null", result.Rows[0][0], result.Rows[0][0])
	}
}

// --- zlib ---

func TestZlibRoundTrip(t *testing.T) {
	result := queryResult(t, `print r = zlib_decompress_from_base64_string(zlib_compress_to_base64_string("Hello, Kusto! This is a test string for compression."))`)
	want := "Hello, Kusto! This is a test string for compression."
	if got := result.Rows[0][0]; got != want {
		t.Errorf("zlib round trip = %v, want %v", got, want)
	}
}

func TestZlibDecompressInvalidReturnsEmptyNotNull(t *testing.T) {
	result := queryResult(t, `print r = zlib_decompress_from_base64_string("also not valid")`)
	if result.Rows[0][0] != "" {
		t.Errorf("r = %v (%T), want empty string, not null", result.Rows[0][0], result.Rows[0][0])
	}
}
