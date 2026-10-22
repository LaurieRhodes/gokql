package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/embed"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// switchingMock serves any model name; vectors are 8 wide.
type switchingMock struct{ model string }

func (m switchingMock) Name() string   { return m.model }
func (m switchingMock) Dimension() int { return 8 }
func (m switchingMock) Embed(ctx context.Context, t string) ([]float64, error) {
	v := make([]float64, 8)
	v[len(t)%8] = 1
	return v, nil
}
func (m switchingMock) EmbedBatch(ctx context.Context, ts []string) ([][]float64, error) {
	return embed.EmbedBatchVia(ctx, m, ts)
}
func (m switchingMock) WithModel(model string) (embed.Embedder, error) {
	return switchingMock{model: model}, nil
}

func modelEngine(t *testing.T, em embed.Embedder) *Engine {
	t.Helper()
	eng := diskEngineEmpty(t)
	eng.SetEmbedder(em)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "one", "b", "two", "c", "three"]`)
	return eng
}

// with (model=...) chooses the model for the run; the stored Model is
// the producing backend's name.
func TestEmbedIntoWithModel(t *testing.T) {
	eng := modelEngine(t, switchingMock{model: "default-model"})
	defer eng.SetEmbedder(nil)
	diskExec(t, eng, `.embed-into E with (model="other-model") <| Docs | project Id, Text`)
	tbl := diskQuery(t, eng, `E | summarize n = count() by Model`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "other-model")
	expectCell(t, tbl, 0, 1, "3")

	// The table now holds other-model: the default model is refused.
	if _, err := runEngine(eng, `.embed-into E <| Docs | extend Id = strcat(Id, "2") | project Id, Text`); err == nil ||
		!strings.Contains(err.Error(), "already holds vectors from model") {
		t.Fatalf("default model into an other-model table: err = %v", err)
	}
}

// A source Model column is a run-level request: one value is
// accepted, several are refused before anything is written, and a
// value that conflicts with with (model=) is refused.
func TestEmbedIntoSourceModelColumn(t *testing.T) {
	eng := modelEngine(t, switchingMock{model: "default-model"})
	defer eng.SetEmbedder(nil)

	_, err := runEngine(eng, `.embed-into E <| Docs | project Id, Text, Model = iff(Id == "c", "m2", "m1")`)
	if err == nil || !strings.Contains(err.Error(), "more than one model") {
		t.Fatalf("mixed Model column: err = %v", err)
	}
	if eng.Catalog.GetTable("E") != nil {
		t.Fatal("a refused run created E")
	}

	_, err = runEngine(eng, `.embed-into E with (model="m2") <| Docs | project Id, Text, Model = "m1"`)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting request: err = %v", err)
	}

	diskExec(t, eng, `.embed-into E <| Docs | project Id, Text, Model = "m1"`)
	expectCell(t, diskQuery(t, eng, `E | summarize n = count() by Model`), 0, 0, "m1")
}

// A source Model column no longer waives the model-identity guard:
// a caller cannot write a second model's vectors into a table by
// naming that model in the query.
func TestEmbedIntoModelColumnDoesNotWaiveIdentity(t *testing.T) {
	eng := modelEngine(t, switchingMock{model: "m1"})
	defer eng.SetEmbedder(nil)
	diskExec(t, eng, `.embed-into E <| Docs | where Id != "c" | project Id, Text`)
	if _, err := runEngine(eng, `.embed-into E <| Docs | where Id == "c" | project Id, Text, Model = "m2"`); err == nil {
		t.Fatal("an m2 run was appended to a table holding m1 vectors")
	}
	expectCell(t, diskQuery(t, eng, `E | summarize n = dcount(Model)`), 0, 0, "1")
}

// A backend that cannot switch models refuses a request for another.
func TestEmbedIntoModelOnStaticBackend(t *testing.T) {
	eng := modelEngine(t, embed.NewMock(8))
	defer eng.SetEmbedder(nil)
	if _, err := runEngine(eng, `.embed-into E with (model="other") <| Docs | project Id, Text`); err == nil ||
		!strings.Contains(err.Error(), "cannot switch models") {
		t.Fatalf("static backend: err = %v", err)
	}
	// Requesting the backend's own model is not a switch.
	diskExec(t, eng, `.embed-into E with (model="mock-8") <| Docs | project Id, Text`)
}

func TestParseEmbedIntoWith(t *testing.T) {
	for q, want := range map[string]string{
		`.embed-into E <| Docs | project Id, Text`:                      "",
		`.embed-into E with (model="nomic") <| Docs | project Id, Text`: "nomic",
		`.embed-into E with(model='x') <| Docs | project Id, Text`:      "x",
	} {
		stmt, err := parser.Parse(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		cmd := stmt.(*parser.EmbedIntoCmd)
		if cmd.TableName != "E" || cmd.Model != want {
			t.Errorf("%s: table %q model %q", q, cmd.TableName, cmd.Model)
		}
	}
	for _, q := range []string{
		`.embed-into E with (batch=3) <| Docs | project Id, Text`,
		`.embed-into E junk <| Docs | project Id, Text`,
	} {
		if _, err := parser.Parse(q); err == nil {
			t.Errorf("%s: expected a parse error", q)
		}
	}
}
