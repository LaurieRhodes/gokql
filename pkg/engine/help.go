package engine

// help.go — .help: lists supported dot-commands as a real, queryable
// table. Found live during the backlog pass: discovering .drop extent
// existed required reading parser source; there was no in-tool way to
// find it. This list is hand-verified against parser.go's dot-command
// prefix checks, not derived automatically, so it needs a manual
// update alongside any future dot-command addition — noted here
// rather than silently drifting.

import "github.com/LaurieRhodes/gokql/pkg/types"

type helpEntry struct{ cmd, desc string }

const chunkFileHelpCmd = `.chunk-file "path" into T`

var helpEntries = []helpEntry{
	{".create table T (Col: Type, ...)", "create an empty table"},
	{".create-merge table T (Col: Type, ...)", "create if absent, or add missing columns"},
	{".set T <| query", "create-only: fails if T already exists"},
	{".set-or-append T <| query", "append query's rows as a new extent, creating T if absent — prefer datatable(...) literals over CSV"},
	{".ingest inline into table T <| row1,row2,...", "append rows given as inline CSV text (no separate file)"},
	{".ingest csv into table T from \"path\"", "append rows from a CSV file"},
	{".drop table T", "remove a table and all its extents"},
	{".drop extent <guid>", "remove one specific extent by id"},
	{".merge table T extents", "compact all of T's extents into one (catalog mode only — refused in discovery mode)"},
	{".compact table T", "discovery mode: merge T's extents into one, superseding (renaming, not deleting) the old ones"},
	{".gc table T", "discovery mode: physically remove files .compact superseded — safe to run any time"},
	{chunkFileHelpCmd, "split a markdown file into paragraph-level chunks and write into T"},
	{".embed-into T <| query", "bulk-embed a Text column via Ollama's batch API (fewer round trips than per-row embed_text)"},
	{".show tables", "list tables with row/extent counts"},
	{".show table T extents", "list T's individual extents"},
	{".show database", "database-level summary"},
	{".help", "this list"},
}

func (e *Engine) showHelp() (*types.Table, error) {
	schema := types.Schema{Columns: []types.Column{
		{Name: "Command", Type: types.TypeString},
		{Name: "Description", Type: types.TypeString},
	}}
	t := types.NewTable("", schema)
	for _, h := range helpEntries {
		t.AddRow(types.Row{h.cmd, h.desc})
	}
	return t, nil
}
