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

const chunkFileHelpCmd = `.chunk-file "path" into T [with (idprefix="key")]`

var helpEntries = []helpEntry{
	{".create table T (Col: Type, ...)", "create an empty table"},
	{".create-merge table T (Col: Type, ...)", "create if absent, or add missing columns"},
	{".set T <| query", "create-only: fails if T already exists"},
	{".set-or-append T <| query", "append query's rows as a new extent, creating T if absent — prefer datatable(...) literals over CSV"},
	{".ingest inline into table T <| row1,row2,...", "append rows given as inline CSV text (no separate file)"},
	{".ingest csv into table T from \"path\"", "append rows from a CSV file"},
	{".drop table T", "remove a table and all its extents"},
	{".drop extent <guid>", "remove one specific extent by id"},
	{".merge table T extents", "merge all of T's extents into one (on disk, the same as .compact table T)"},
	{".compact table T [with (vectors=compact)]", "discovery mode: merge T's extents (wide rows into several), superseding (renaming, not deleting) the old ones; vectors=compact re-encodes embedding columns from JSON to compact float32"},
	{".gc table T", "discovery mode: physically remove files .compact superseded — safe to run any time"},
	{chunkFileHelpCmd, "split a markdown file into paragraph-level chunks and write into T"},
	{".embed-into T [with (model=\"name\")] <| query", "bulk-embed the query's Text column into T, resumably; one model per table"},
	{".create-or-alter graph_model M ```{json}```", "define a graph model (AddNodes/AddEdges steps); each call adds a version"},
	{".make [async] graph_snapshot S from M", "freeze graph model M into snapshot S; graph(\"M\"[, \"S\"]) queries it"},
	{".show graph_models / .show graph_model M [details] [with (id=\"*\")]", "list graph models or one model's versions"},
	{".show graph_snapshots M|* / .show graph_snapshot M.S [details]", "list graph snapshots"},
	{".drop graph_model M / .drop graph_snapshot M.S / .drop graph_snapshots M", "drop a model (with its snapshots) or snapshots"},
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
