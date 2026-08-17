package engine

import "testing"

// func_parse_command_line_test.go — parse_command_line, added
// 2026-08-17 (func_net.go), implementing the standard Win32
// CommandLineToArgvW tokenization algorithm. Verified exactly against
// real ADX's own worked example.

func TestParseCommandLineWorkedExample(t *testing.T) {
	result := queryResult(t, `print r = parse_command_line('echo "hello world!"', "windows")`)
	got := result.Rows[0][0].(string)
	want := `["echo","hello world!"]`
	if got != want {
		t.Errorf("parse_command_line = %v, want %v", got, want)
	}
}

func TestParseCommandLineBasicWhitespace(t *testing.T) {
	result := queryResult(t, `print r = parse_command_line("a b c", "windows")`)
	got := result.Rows[0][0].(string)
	want := `["a","b","c"]`
	if got != want {
		t.Errorf("parse_command_line = %v, want %v", got, want)
	}
}

// TestParseCommandLineBackslashRules exercises the CommandLineToArgvW
// backslash-before-quote rules directly: an even run of N backslashes
// before a quote emits N/2 literal backslashes and the quote toggles
// quoting (the quote character itself doesn't appear in output).
// Verified live before writing this assertion (raw command-line text
// confirmed to be exactly 8 characters via strlen: a \ \ " b space c ").
func TestParseCommandLineBackslashRules(t *testing.T) {
	result := queryResult(t, `print r = parse_command_line('a\\\\"b c"', "windows")`)
	got := result.Rows[0][0].(string)
	want := `["a\\b c"]`
	if got != want {
		t.Errorf("parse_command_line backslash rule = %v, want %v", got, want)
	}
}

func TestParseCommandLineUnsupportedParserType(t *testing.T) {
	result := queryResult(t, `print r = parse_command_line("a b c", "posix")`)
	if result.Rows[0][0] != nil {
		t.Errorf("parse_command_line with unsupported parser_type = %v, want nil", result.Rows[0][0])
	}
}

func TestParseCommandLineNullPropagation(t *testing.T) {
	result := queryResult(t, `print a = parse_command_line(dynamic(null), "windows"), b = parse_command_line("a b c", dynamic(null))`)
	if result.Rows[0][0] != nil {
		t.Errorf("parse_command_line(null, ...) = %v, want nil", result.Rows[0][0])
	}
	if result.Rows[0][1] != nil {
		t.Errorf("parse_command_line(..., null) = %v, want nil", result.Rows[0][1])
	}
}

