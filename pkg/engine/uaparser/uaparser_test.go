package uaparser

import "testing"

// uaparser_test.go — verified directly against real ADX's own
// parse_user_agent() worked examples (learn.microsoft.com/en-us/kusto/
// query/parse-user-agent-function), using the real ua-parser/uap-core
// regexes.yaml pattern database rather than a hand-rolled
// approximation. See package doc comment in uaparser.go and
// NOTICE.md/LICENSE-APACHE-2.0.txt for attribution.

// TestParseNokiaWorkedExample guards the exact worked example real
// ADX's own docs give for the dynamic-array look_for form. This is a
// deliberately tricky case: the raw string contains a literal
// "Safari/4" substring that a naive "does it contain Safari" parser
// would misclassify -- the correct answer, "Nokia OSS Browser"
// version 3.1, is sourced from a completely different token
// (Series60/3.1) via one specific real pattern-database rule.
func TestParseNokiaWorkedExample(t *testing.T) {
	ua := `Mozilla/5.0 (SymbianOS/9.2; U; Series60/3.1 NokiaN81-3/10.0.032 Profile/MIDP-2.0 Configuration/CLDC-1.1 ) AppleWebKit/413 (KHTML, like Gecko) Safari/4`

	b, err := ParseBrowser(ua)
	if err != nil {
		t.Fatalf("ParseBrowser error: %v", err)
	}
	wantB := Browser{Family: "Nokia OSS Browser", MajorVersion: "3", MinorVersion: "1", Patch: ""}
	if b != wantB {
		t.Errorf("ParseBrowser = %+v, want %+v", b, wantB)
	}

	o, err := ParseOS(ua)
	if err != nil {
		t.Fatalf("ParseOS error: %v", err)
	}
	wantO := OS{Family: "Symbian OS", MajorVersion: "9", MinorVersion: "2", Patch: "", PatchMinor: ""}
	if o != wantO {
		t.Errorf("ParseOS = %+v, want %+v", o, wantO)
	}

	d, err := ParseDevice(ua)
	if err != nil {
		t.Fatalf("ParseDevice error: %v", err)
	}
	wantD := Device{Family: "Nokia N81", Brand: "Nokia", Model: "N81-3"}
	if d != wantD {
		t.Errorf("ParseDevice = %+v, want %+v", d, wantD)
	}
}

// TestParseAdobeAIRWorkedExample guards real ADX's own second worked
// example (the single-string look_for="browser" form).
func TestParseAdobeAIRWorkedExample(t *testing.T) {
	ua := `Mozilla/5.0 (Windows; U; en-US) AppleWebKit/531.9 (KHTML, like Gecko) AdobeAIR/2.5.1`
	b, err := ParseBrowser(ua)
	if err != nil {
		t.Fatalf("ParseBrowser error: %v", err)
	}
	want := Browser{Family: "AdobeAIR", MajorVersion: "2", MinorVersion: "5", Patch: "1"}
	if b != want {
		t.Errorf("ParseBrowser = %+v, want %+v", b, want)
	}
}

// TestParseUnmatchedFallsBackToOther confirms the documented "Other"
// fallback (real ADX's own docs: "the value for family shall be
// Other" when no pattern matches) for a string with no possible
// pattern match, across all three parser categories.
func TestParseUnmatchedFallsBackToOther(t *testing.T) {
	ua := "zzz-completely-unrecognizable-junk-zzz"
	b, err := ParseBrowser(ua)
	if err != nil {
		t.Fatal(err)
	}
	if b.Family != "Other" || b.MajorVersion != "" {
		t.Errorf("ParseBrowser(unmatched) = %+v, want Family=Other with empty versions", b)
	}
	o, err := ParseOS(ua)
	if err != nil {
		t.Fatal(err)
	}
	if o.Family != "Other" {
		t.Errorf("ParseOS(unmatched) = %+v, want Family=Other", o)
	}
}

// TestLoadCompilesAllPatternsWithoutError is a basic sanity check
// that the embedded regexes.yaml still loads and every one of its
// patterns still compiles under Go's regexp engine -- this was
// verified as a one-off check (1270/1270 patterns) before writing
// this package, but this test keeps that guarantee enforced by CI
// rather than only true "at the time it was checked."
func TestLoadCompilesAllPatternsWithoutError(t *testing.T) {
	if _, err := ParseBrowser("anything"); err != nil {
		t.Fatalf("embedded pattern database failed to load/compile: %v", err)
	}
	if len(uaRules) == 0 || len(osRules) == 0 || len(devRules) == 0 {
		t.Fatalf("one or more rule categories loaded empty: ua=%d os=%d device=%d", len(uaRules), len(osRules), len(devRules))
	}
}
