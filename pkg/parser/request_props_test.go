package parser

import "testing"

func TestRequestPropertiesQueryLanguage(t *testing.T) {
	props, rest := splitRequestProperties("#crp query_language=gql\n  #crp query_graph_reference = 'G'\nmatch (n) return n")
	if props["query_language"] != "gql" || props["query_graph_reference"] != "G" || rest != "match (n) return n" {
		t.Fatalf("got %v %q", props, rest)
	}
	if _, err := Parse("#crp query_language=gql\n#crp query_graph_reference=G\nmatch (p:Person) return p.Name"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("#crp query_language=sql\nselect 1"); err == nil {
		t.Fatal("expected an unsupported-language error")
	}
	// Other properties leave a KQL query alone.
	if _, err := Parse("#crp truncationmaxrecords=10\nprint 1"); err != nil {
		t.Fatal(err)
	}
}
