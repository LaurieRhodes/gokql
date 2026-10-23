package main

import (
	"reflect"
	"testing"
)

func TestScriptStatements(t *testing.T) {
	in := `.set-or-append T <| datatable(a:long, b:string)
  [1, "x",

   2, "y"]

let n = 1;
T
| where a > n
// comment
T | count
print s = "a // not a comment"
.create-or-alter graph_model M ` + "```" + `
{ "Schema": {}
}
` + "```" + `
print 2
#crp query_language=gql
MATCH (a)-->(b)
RETURN a

print 3`
	want := []string{
		".set-or-append T <| datatable(a:long, b:string)\n  [1, \"x\",\n\n   2, \"y\"]",
		"let n = 1;\nT\n| where a > n",
		"T | count",
		`print s = "a // not a comment"`,
		".create-or-alter graph_model M ```\n{ \"Schema\": {}\n}\n```",
		"print 2",
		"#crp query_language=gql\nMATCH (a)-->(b)\nRETURN a",
		"print 3",
	}
	if got := scriptStatements(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
