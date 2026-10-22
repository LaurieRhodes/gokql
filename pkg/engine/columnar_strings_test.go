package engine

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// strEngine builds a three-extent table for the columnar string
// predicates and dcount. The third extent is written after
// .alter-merge adds Late, so earlier extents have no Late cells.
func strEngine(t *testing.T) *Engine {
	t.Helper()
	names := []string{"Foo", "foo", "FOO-bar", "foo_bar", "x.y", "Ärger", "ÄRGER",
		"alpha beta", "Beta", "", "a%b", "end.", "preFoo", "10.0.0.1"}
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table S (Id: long, Name: string, Msg: string, Flag: bool, Score: real)`)
	row := func(id int) string {
		n := names[id%len(names)]
		return fmt.Sprintf("%d,%s,%s evt %d,%t,%s", id, n, n, id*7919%1000, id%3 == 0,
			[]string{"0", "1.5", "-2", "1.5", "3"}[id%5])
	}
	for ext := 0; ext < 2; ext++ {
		rows := make([]string, 0, 200)
		for i := 0; i < 200; i++ {
			rows = append(rows, row(ext*200+i))
		}
		diskExec(t, eng, ".ingest inline into table S <| "+joinLines(rows))
	}
	diskExec(t, eng, `.alter-merge table S (Late: string)`)
	rows := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		id := 400 + i
		rows = append(rows, row(id)+","+names[(id*3)%len(names)])
	}
	diskExec(t, eng, ".ingest inline into table S <| "+joinLines(rows))
	return eng
}

// TestColumnarStringPredicatesDifferential checks string-operator
// predicates and dcount on the columnar paths against the row engine
// (forced by a `where 1 == 1` prefix the planner cannot take).
func TestColumnarStringPredicatesDifferential(t *testing.T) {
	eng := strEngine(t)
	preds := []string{
		`Name has "foo"`, `Name !has "foo"`, `Name has_cs "foo"`, `Name !has_cs "Foo"`,
		`Name contains "oo"`, `Name !contains "OO"`, `Name contains_cs "oo"`, `Name !contains_cs "oo"`,
		`Name startswith "fo"`, `Name !startswith "FO"`, `Name startswith_cs "Fo"`, `Name !startswith_cs "Fo"`,
		`Name endswith "BAR"`, `Name !endswith "bar"`, `Name endswith_cs "bar"`, `Name !endswith_cs "bar"`,
		`Name hasprefix "pre"`, `Name !hasprefix "al"`, `Name hasprefix_cs "Be"`, `Name !hasprefix_cs "Be"`,
		`Name hassuffix "ta"`, `Name !hassuffix "ta"`, `Name hassuffix_cs "ta"`, `Name !hassuffix_cs "ta"`,
		`Name matches regex "^[Ff]oo"`, `Name matches regex "ä|Ä"`,
		`Name =~ "foo"`, `Name !~ "FOO"`, `Name =~ "ärger"`,
		`Name has "ärger"`, `Name contains "ÄR"`, `Name has ""`, `Name startswith ""`,
		`Msg has "evt"`, `Msg has "123"`, `Msg !has "1"`, `Msg contains "9 "`, `Msg endswith "7"`,
		`Name has "foo" and Id > 100`, `Name !has "x" and Msg contains "evt 5"`,
		// Late is absent from the first two extents: those rows are
		// the row engine's nil operand.
		`Late has "foo"`, `Late !has "foo"`, `Late startswith ""`, `Late !contains "zz"`,
		`Late =~ "foo"`, `Late !~ "foo"`, `Late == ""`,
	}
	tails := []string{
		`| summarize C = count()`,
		`| summarize C = count(), D = dcount(Msg) by Name`,
		`| count`,
		`| top 7 by Id desc`,
	}
	var queries []string
	for _, p := range preds {
		for _, tl := range tails {
			queries = append(queries, `S | where `+p+` `+tl)
		}
	}
	queries = append(queries,
		`S | summarize D = dcount(Name)`,
		`S | summarize D = dcount(Msg), E = count_distinct(Name) by Flag`,
		`S | summarize D = dcount(Score), F = dcount(Flag), I = dcount(Id) by Name`,
		`S | summarize D = dcount(Late), C = count() by Name`,
		`S | where Score > 0.0 | summarize D = dcount(Late) by Flag`,
		`S | summarize D = dcount(Name), M = max(Id), S = sum(Score) by Flag`,
	)

	for _, q := range queries {
		fast := tableCSV(t, diskQuery(t, eng, q))
		slow := tableCSV(t, diskQuery(t, eng, `S | where 1 == 1 `+q[strings.Index(q, "|"):]))
		if fast != slow {
			t.Errorf("differential mismatch for %q:\ncolumnar:\n%s\nrow engine:\n%s", q, fast, slow)
		}
	}
}

// TestColumnarStringPlanned pins that these shapes take the columnar
// paths (the differential test alone would pass if they silently fell
// back) and that the shapes left on the row path still do.
func TestColumnarStringPlanned(t *testing.T) {
	eng := strEngine(t)
	def := eng.Catalog.GetTable("S")
	if def == nil {
		t.Fatal("table S missing")
	}
	plan := func(q string) *colAggPlan {
		stmt, err := parser.Parse(q)
		if err != nil {
			t.Fatal(err)
		}
		return planColumnarAggregate(stmt.(*parser.Query).Operators, &def.Schema)
	}
	for _, q := range []string{
		`S | where Name has "foo" | count`,
		`S | where Msg contains "x" and Id > 3 | summarize count() by Name`,
		`S | where Name matches regex "^a" | summarize dcount(Msg)`,
		`S | where Name =~ "foo" | summarize count()`,
		`S | summarize dcount(Name), count_distinct(Score) by Flag`,
	} {
		if plan(q) == nil {
			t.Errorf("expected columnar plan: %s", q)
		}
	}
	for _, q := range []string{
		`S | where "foo" has Name | count`,         // literal on the left
		`S | where Name matches regex "(" | count`, // invalid regex: row path reports it
		`S | where Score has "1" | count`,          // non-string column
		`S | summarize dcount(Name, 2)`,            // accuracy argument
		`S | summarize dcount(strcat(Name, Msg))`,  // expression argument
	} {
		if plan(q) != nil {
			t.Errorf("expected row-engine fallback: %s", q)
		}
	}
	// The invalid regex still errors through the row path.
	if _, err := eng.Execute(parseStmt(t, `S | where Name matches regex "(" | count`)); err == nil {
		t.Error("invalid regex: expected an error")
	}
}

func parseStmt(t *testing.T, q string) parser.Statement {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	return stmt
}

// TestDistinctNumKeyMatchesSprintf: the columnar dcount key must split
// and merge values exactly where fmt.Sprintf("%v") does.
func TestDistinctNumKeyMatchesSprintf(t *testing.T) {
	vals := []float64{0, math.Copysign(0, -1), 1.5, 1.5000000000000002, math.NaN(), -math.NaN(),
		math.Float64frombits(0x7ff8000000000123), math.Inf(1), math.Inf(-1), 1e21, 1e-300}
	c := &colVec{f64: vals}
	for i := range vals {
		for j := range vals {
			ki, _ := distinctNumKey(c, i)
			kj, _ := distinctNumKey(c, j)
			si, sj := fmt.Sprintf("%v", vals[i]), fmt.Sprintf("%v", vals[j])
			if (ki == kj) != (si == sj) {
				t.Errorf("%v vs %v: key equal %v, Sprintf equal %v", vals[i], vals[j], ki == kj, si == sj)
			}
		}
	}
}

// TestCompileStringMatcherAgrees: the hoisted matcher and the
// reference stringOpMatch agree on every operator.
func TestCompileStringMatcherAgrees(t *testing.T) {
	ops := []parser.BinaryOp{
		parser.OpContains, parser.OpNotContains, parser.OpContainsCS, parser.OpNotContainsCS,
		parser.OpHas, parser.OpNotHas, parser.OpHasCS, parser.OpNotHasCS,
		parser.OpStartsWith, parser.OpNotStartsWith, parser.OpStartsWithCS, parser.OpNotStartsWithCS,
		parser.OpEndsWith, parser.OpNotEndsWith, parser.OpEndsWithCS, parser.OpNotEndsWithCS,
		parser.OpHasPrefix, parser.OpNotHasPrefix, parser.OpHasPrefixCS, parser.OpNotHasPrefixCS,
		parser.OpHasSuffix, parser.OpNotHasSuffix, parser.OpHasSuffixCS, parser.OpNotHasSuffixCS,
		parser.OpLike, parser.OpNotLike, parser.OpMatchesRegex, parser.OpCIEQ, parser.OpCINEQ,
	}
	subjects := []string{"", "Foo", "foo bar", "FOO-BAR", "xfoo", "Ärger", "ärger", "İstanbul", "a%b", "end."}
	literals := []string{"", "foo", "FOO", "o b", "ärger", "ÄR", "i̇stanbul", "%", "a%", ".", "^f"}
	for _, op := range ops {
		for _, lit := range literals {
			m, err := compileStringMatcher(op, lit)
			if err != nil {
				t.Fatalf("op %v %q: %v", op, lit, err)
			}
			for _, s := range subjects {
				want, _ := stringOpMatch(op, s, lit)
				if got := m(s); got != want {
					t.Errorf("op %v: %q vs %q: matcher %v, reference %v", op, s, lit, got, want)
				}
			}
		}
	}
}
