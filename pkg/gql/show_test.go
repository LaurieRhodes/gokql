package gql

import (
	"os"
	"testing"
)

// TestShow prints the translation of $GQL_SHOW (openCypher when
// $GQL_CYPHER is set): a debugging aid.
func TestShow(t *testing.T) {
	src := os.Getenv("GQL_SHOW")
	if src == "" {
		t.Skip("set GQL_SHOW")
	}
	out, err := Translate(src, "G", os.Getenv("GQL_CYPHER") != "")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + out)
}
