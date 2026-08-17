package engine

import "testing"

// func_parse_user_agent_test.go — parse_user_agent, added 2026-08-18
// (func_net.go), backed by the real ua-parser/uap-core pattern
// database (pkg/engine/uaparser). See that package's own test file
// for direct Parse{Browser,OS,Device} coverage; these tests cover the
// KQL-level wiring: argument handling, output-category selection, and
// the null/error conventions this file's other functions already
// establish.

// TestParseUserAgentNokiaWorkedExample guards the exact worked
// example from the person's own conversation (also present in real
// ADX's own docs): the dynamic-array look_for form requesting all
// three categories.
func TestParseUserAgentNokiaWorkedExample(t *testing.T) {
	result := queryResult(t, `print useragent = "Mozilla/5.0 (SymbianOS/9.2; U; Series60/3.1 NokiaN81-3/10.0.032 Profile/MIDP-2.0 Configuration/CLDC-1.1 ) AppleWebKit/413 (KHTML, like Gecko) Safari/4"
		| extend x = parse_user_agent(useragent, dynamic(["browser","os","device"]))`)
	got := result.Rows[0][1].(string)
	want := `{"Browser":{"Family":"Nokia OSS Browser","MajorVersion":"3","MinorVersion":"1","Patch":""},"OperatingSystem":{"Family":"Symbian OS","MajorVersion":"9","MinorVersion":"2","Patch":"","PatchMinor":""},"Device":{"Family":"Nokia N81","Brand":"Nokia","Model":"N81-3"}}`
	if got != want {
		t.Errorf("parse_user_agent =\n%v\nwant\n%v", got, want)
	}
}

// TestParseUserAgentAdobeAIRSingleStringLookFor guards real ADX's own
// second worked example: look_for as a single string (not a dynamic
// array), which must return ONLY the requested category (no
// OperatingSystem/Device keys at all).
func TestParseUserAgentAdobeAIRSingleStringLookFor(t *testing.T) {
	result := queryResult(t, `print useragent = "Mozilla/5.0 (Windows; U; en-US) AppleWebKit/531.9 (KHTML, like Gecko) AdobeAIR/2.5.1"
		| extend x = parse_user_agent(useragent, "browser")`)
	got := result.Rows[0][1].(string)
	want := `{"Browser":{"Family":"AdobeAIR","MajorVersion":"2","MinorVersion":"5","Patch":"1"}}`
	if got != want {
		t.Errorf("parse_user_agent =\n%v\nwant\n%v", got, want)
	}
}

// TestParseUserAgentUnmatchedFallsBackToOther confirms the KQL-level
// wiring surfaces the documented "Other" fallback correctly.
func TestParseUserAgentUnmatchedFallsBackToOther(t *testing.T) {
	result := queryResult(t, `print x = parse_user_agent("zzz-not-a-real-agent-zzz", "browser")`)
	got := result.Rows[0][0].(string)
	want := `{"Browser":{"Family":"Other","MajorVersion":"","MinorVersion":"","Patch":""}}`
	if got != want {
		t.Errorf("parse_user_agent(unmatched) = %v, want %v", got, want)
	}
}

// TestParseUserAgentNullPropagation confirms a null argument (either
// position) propagates as null, the convention this file's other
// parse_* functions already establish.
func TestParseUserAgentNullPropagation(t *testing.T) {
	result := queryResult(t, `print a = parse_user_agent(dynamic(null), "browser"), b = parse_user_agent("some ua", dynamic(null))`)
	if result.Rows[0][0] != nil {
		t.Errorf("parse_user_agent(null, ...) = %v, want nil", result.Rows[0][0])
	}
	if result.Rows[0][1] != nil {
		t.Errorf("parse_user_agent(..., null) = %v, want nil", result.Rows[0][1])
	}
}
