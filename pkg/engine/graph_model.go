package engine

// graph_model.go — persistent graph models and snapshots.
//
// A graph model is a JSON definition (optional Schema of node and edge
// label types; Definition.Steps of AddNodes / AddEdges, each a KQL
// query) stored as versions in the _GraphModels system table. A
// snapshot materialises a model at a point in time into
// _GraphSnapshotNodes / _GraphSnapshotEdges, described by a row in
// _GraphSnapshots. graph("Model") reads the model's latest snapshot, or
// builds the graph from the model's latest version when it has none;
// graph("Model", "Snapshot") reads that snapshot.
//
// All four tables are append-only, like _Functions and
// _MaterializedViews: the latest row per name wins and drops are
// Deleted tombstones, so concurrent writers only ever add extents.
// Snapshot data is keyed by a per-snapshot GUID, so recreating a
// dropped snapshot name never mixes old rows into the new one; rows of
// dropped snapshots stay on disk (soft delete).

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const (
	graphModelsTable        = "_GraphModels"
	graphSnapshotsTable     = "_GraphSnapshots"
	graphSnapshotNodesTable = "_GraphSnapshotNodes"
	graphSnapshotEdgesTable = "_GraphSnapshotEdges"
	graphSnapshotFailures   = "_GraphSnapshotFailures"
	graphModelPolicies      = "_GraphModelPolicies"
)

func graphSystemSchemas() map[string]types.Schema {
	s, l, dt, b, dyn, ts := types.TypeString, types.TypeLong, types.TypeDatetime, types.TypeBool, types.TypeDynamic, types.TypeTimespan
	col := func(n string, t types.KQLType) types.Column { return types.Column{Name: n, Type: t} }
	return map[string]types.Schema{
		graphModelsTable: {Columns: []types.Column{
			col("Name", s), col("Id", s), col("Model", s), col("Deleted", b), col("CreatedAt", dt)}},
		graphSnapshotsTable: {Columns: []types.Column{
			col("Name", s), col("ModelName", s), col("ModelId", s), col("ModelCreationTime", dt),
			col("SnapshotId", s), col("NodesCount", l), col("EdgesCount", l), col("Layout", s),
			col("Deleted", b), col("CreatedAt", dt), col("Statistics", s)}},
		graphSnapshotFailures: {Columns: []types.Column{
			col("OperationId", s), col("ModelName", s), col("ModelId", s), col("Name", s),
			col("SnapshotTime", dt), col("Duration", ts), col("FailureReason", s), col("FailureKind", s),
			col("Details", s)}},
		graphModelPolicies: {Columns: []types.Column{
			col("ModelName", s), col("PolicyName", s), col("Policy", s), col("Deleted", b), col("CreatedAt", dt)}},
		graphSnapshotNodesTable: {Columns: []types.Column{
			col("SnapshotId", s), col("Id", s), col("Labels", dyn), col("Properties", s)}},
		graphSnapshotEdgesTable: {Columns: []types.Column{
			col("SnapshotId", s), col("Source", s), col("Target", s), col("Labels", dyn), col("Properties", s)}},
	}
}

func (e *Engine) ensureGraphTable(name string) error {
	if e.Catalog.GetTable(name) != nil {
		return nil
	}
	schema := graphSystemSchemas()[name]
	if err := e.Catalog.CreateTable(name, schema); err != nil {
		return err
	}
	return e.persistDiscoverySchema(name, schema)
}

func (e *Engine) appendGraphRows(table string, rows []types.Row) error {
	if len(rows) == 0 {
		return nil
	}
	if err := e.ensureGraphTable(table); err != nil {
		return err
	}
	_, err := e.flushBatch(table, e.Catalog.GetTable(table), rows)
	return err
}

// readGraphTable returns the rows of a graph system table, optionally
// filtered by a KQL where clause; nil when the table does not exist.
func (e *Engine) readGraphTable(table, where string) (*types.Table, error) {
	if e.Catalog.GetTable(table) == nil {
		return nil, nil
	}
	q := table
	if where != "" {
		q += " | where " + where
	}
	stmt, err := parser.Parse(q)
	if err != nil {
		return nil, err
	}
	return e.executeQuery(stmt.(*parser.Query))
}

func newGUIDString() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck // crypto/rand does not fail
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func kqlStringLiteral(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- model definitions ---

type graphModelDef struct {
	Schema *struct {
		Nodes map[string]map[string]string `json:"Nodes"`
		Edges map[string]map[string]string `json:"Edges"`
	} `json:"Schema"`
	Definition struct {
		Steps []graphModelStep `json:"Steps"`
	} `json:"Definition"`
}

type graphModelStep struct {
	Kind         string   `json:"Kind"`
	Query        string   `json:"Query"`
	NodeIdColumn string   `json:"NodeIdColumn"`
	SourceColumn string   `json:"SourceColumn"`
	TargetColumn string   `json:"TargetColumn"`
	Labels       []string `json:"Labels"`
	LabelsColumn string   `json:"LabelsColumn"`
}

// parseGraphModelDef decodes and validates a model definition. Step
// queries are parsed (not run): a model may name tables that are
// filled later.
func parseGraphModelDef(payload string) (*graphModelDef, error) {
	payload = escapeJSONStringControls(payload)
	var def graphModelDef
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("invalid graph model definition: %w", err)
	}
	if len(def.Definition.Steps) == 0 {
		return nil, fmt.Errorf("invalid graph model definition: Definition.Steps is empty")
	}
	for i, st := range def.Definition.Steps {
		where := fmt.Sprintf("step %d (%s)", i+1, st.Kind)
		var declared map[string]map[string]string
		switch st.Kind {
		case "AddNodes":
			if st.NodeIdColumn == "" {
				return nil, fmt.Errorf("graph model %s: NodeIdColumn is required", where)
			}
			if def.Schema != nil {
				declared = def.Schema.Nodes
			}
		case "AddEdges":
			if st.SourceColumn == "" || st.TargetColumn == "" {
				return nil, fmt.Errorf("graph model %s: SourceColumn and TargetColumn are required", where)
			}
			if def.Schema != nil {
				declared = def.Schema.Edges
			}
		default:
			return nil, fmt.Errorf("graph model step %d: Kind must be AddNodes or AddEdges, got %q", i+1, st.Kind)
		}
		if strings.TrimSpace(st.Query) == "" {
			return nil, fmt.Errorf("graph model %s: Query is required", where)
		}
		if _, err := parser.Parse(st.Query); err != nil {
			return nil, fmt.Errorf("graph model %s: query: %w", where, err)
		}
		if def.Schema != nil {
			for _, l := range st.Labels {
				if _, ok := declared[l]; !ok {
					return nil, fmt.Errorf("graph model %s: label %q is not declared in Schema", where, l)
				}
			}
		}
	}
	return &def, nil
}

// escapeJSONStringControls relaxes a model definition the way ADX's
// JSON reader does: raw line breaks and tabs inside strings (a step's
// Query written over several lines) are escaped, and trailing commas
// before } or ] are dropped.
func escapeJSONStringControls(s string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !in && c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		if in {
			switch c {
			case '\\':
				b.WriteByte(c)
				if i+1 < len(s) {
					i++
					b.WriteByte(s[i])
				}
				continue
			case '"':
				in = false
			case '\n':
				b.WriteString(`\n`)
				continue
			case '\r':
				b.WriteString(`\r`)
				continue
			case '\t':
				b.WriteString(`\t`)
				continue
			}
		} else if c == '"' {
			in = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// graphModelVersion is one stored version of a model.
type graphModelVersion struct {
	Name    string
	Id      string
	Model   string
	Created int64
}

// graphModelVersions returns every live version of every model (or of
// name, when set), oldest first. A tombstone ends a model's history:
// versions before it are gone.
func (e *Engine) graphModelVersions(name string) (map[string][]graphModelVersion, []string, error) {
	where := ""
	if name != "" {
		where = "Name == " + kqlStringLiteral(name)
	}
	t, err := e.readGraphTable(graphModelsTable, where)
	if err != nil || t == nil {
		return nil, nil, err
	}
	rows := append([]types.Row(nil), t.Rows...)
	ci := t.Schema.ColumnIndex
	nameI, idI, modelI, delI, createdI := ci("Name"), ci("Id"), ci("Model"), ci("Deleted"), ci("CreatedAt")
	sort.SliceStable(rows, func(a, b int) bool { return types.ToInt64(rows[a][createdI]) < types.ToInt64(rows[b][createdI]) })
	out := make(map[string][]graphModelVersion)
	var order []string
	for _, r := range rows {
		n := fmt.Sprint(r[nameI])
		if _, seen := out[n]; !seen {
			order = append(order, n)
			out[n] = nil
		}
		if del, _ := r[delI].(bool); del {
			out[n] = out[n][:0]
			continue
		}
		out[n] = append(out[n], graphModelVersion{Name: n, Id: fmt.Sprint(r[idI]), Model: fmt.Sprint(r[modelI]), Created: types.ToInt64(r[createdI])})
	}
	live := order[:0]
	for _, n := range order {
		if len(out[n]) > 0 {
			live = append(live, n)
		} else {
			delete(out, n)
		}
	}
	return out, live, nil
}

func (e *Engine) latestGraphModel(name string) (*graphModelVersion, error) {
	all, _, err := e.graphModelVersions(name)
	if err != nil {
		return nil, err
	}
	vs := all[name]
	if len(vs) == 0 {
		return nil, fmt.Errorf("graph model %q not found", name)
	}
	v := vs[len(vs)-1]
	return &v, nil
}

// --- snapshots ---

type graphSnapshotInfo struct {
	Name, ModelName, ModelId, SnapshotId, Layout string
	ModelCreated, Created                        int64
	Nodes, Edges                                 int64
	Statistics                                   string // JSON graphSnapshotStats
}

// graphStepStat and graphSnapshotStats are what .show graph_snapshot
// statistics reports about how a snapshot was built.
type graphStepStat struct {
	Kind               string
	StepNumber         int
	Status             string
	Duration           string
	StartTime          string
	AddedElementsCount int64
}

type graphSnapshotStats struct {
	Duration  int64 // ticks
	NodesSize int64
	EdgesSize int64
	Steps     []graphStepStat
}

// graphSnapshots returns the live snapshots of model ("*" = all),
// ordered by name, then creation time.
func (e *Engine) graphSnapshots(model string) ([]graphSnapshotInfo, error) {
	where := ""
	if model != "*" {
		where = "ModelName == " + kqlStringLiteral(model)
	}
	t, err := e.readGraphTable(graphSnapshotsTable, where)
	if err != nil || t == nil {
		return nil, err
	}
	ci := t.Schema.ColumnIndex
	rows := append([]types.Row(nil), t.Rows...)
	createdI := ci("CreatedAt")
	sort.SliceStable(rows, func(a, b int) bool { return types.ToInt64(rows[a][createdI]) < types.ToInt64(rows[b][createdI]) })
	latest := make(map[string]graphSnapshotInfo)
	deleted := make(map[string]bool)
	var keys []string
	for _, r := range rows {
		info := graphSnapshotInfo{
			Name: fmt.Sprint(r[ci("Name")]), ModelName: fmt.Sprint(r[ci("ModelName")]),
			ModelId: fmt.Sprint(r[ci("ModelId")]), SnapshotId: fmt.Sprint(r[ci("SnapshotId")]),
			Layout: fmt.Sprint(r[ci("Layout")]), ModelCreated: types.ToInt64(r[ci("ModelCreationTime")]),
			Created: types.ToInt64(r[createdI]), Nodes: types.ToInt64(r[ci("NodesCount")]), Edges: types.ToInt64(r[ci("EdgesCount")]),
		}
		if si := ci("Statistics"); si >= 0 && r[si] != nil {
			info.Statistics = fmt.Sprint(r[si])
		}
		k := info.ModelName + "\x00" + info.Name
		if _, seen := latest[k]; !seen {
			keys = append(keys, k)
		}
		latest[k] = info
		del, _ := r[ci("Deleted")].(bool)
		deleted[k] = del
	}
	var out []graphSnapshotInfo
	for _, k := range keys {
		if !deleted[k] {
			out = append(out, latest[k])
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].Created < out[b].Created
	})
	return out, nil
}

func (e *Engine) findGraphSnapshot(model, name string) (*graphSnapshotInfo, error) {
	snaps, err := e.graphSnapshots(model)
	if err != nil {
		return nil, err
	}
	for i := range snaps {
		if snaps[i].Name == name {
			return &snaps[i], nil
		}
	}
	return nil, fmt.Errorf("graph snapshot %s.%s not found", model, name)
}

// graphLayout records a snapshot's property schemas and id type, so its
// rows decode back to typed values.
type graphLayout struct {
	IdType string
	Nodes  [][2]string
	Edges  [][2]string
}

func layoutColumns(s types.Schema) [][2]string {
	out := make([][2]string, len(s.Columns))
	for i, c := range s.Columns {
		out[i] = [2]string{c.Name, c.Type.String()}
	}
	return out
}

func schemaFromLayout(cols [][2]string) (types.Schema, error) {
	var s types.Schema
	for _, c := range cols {
		t, err := types.ParseType(c[1])
		if err != nil {
			return s, err
		}
		s.Columns = append(s.Columns, types.Column{Name: c[0], Type: t})
	}
	return s, nil
}

// encodeGraphValue maps a value to JSON losslessly: integers stay
// integers, non-finite reals and decimals become strings.
func encodeGraphValue(v types.Value) interface{} {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return strconv.FormatFloat(x, 'g', -1, 64)
		}
		return x
	case types.Decimal:
		return x.String()
	}
	return v
}

func decodeGraphValue(v interface{}, t types.KQLType) types.Value {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case json.Number:
		switch t {
		case types.TypeLong, types.TypeDatetime, types.TypeTimespan:
			if i, err := x.Int64(); err == nil {
				return i
			}
		case types.TypeInt:
			if i, err := x.Int64(); err == nil {
				return int32(i)
			}
		case types.TypeDecimal:
			if d, ok := types.ParseDecimal(x.String()); ok {
				return d
			}
		}
		f, _ := x.Float64()
		return f
	case string:
		switch t {
		case types.TypeReal:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return f
			}
		case types.TypeDecimal:
			if d, ok := types.ParseDecimal(x); ok {
				return d
			}
		}
		return x
	}
	return v
}

func encodeGraphRow(row types.Row) string {
	vals := make([]interface{}, len(row))
	for i, v := range row {
		vals[i] = encodeGraphValue(v)
	}
	b, _ := json.Marshal(vals)
	return string(b)
}

func decodeGraphJSON(s string) (interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v interface{}
	err := dec.Decode(&v)
	return v, err
}

func decodeGraphRow(s string, schema types.Schema) (types.Row, error) {
	v, err := decodeGraphJSON(s)
	if err != nil {
		return nil, err
	}
	arr, _ := v.([]interface{})
	row := make(types.Row, len(schema.Columns))
	for i := range row {
		if i < len(arr) {
			row[i] = decodeGraphValue(arr[i], schema.Columns[i].Type)
		}
	}
	return row, nil
}

func labelsJSON(labels []string) string {
	if len(labels) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(labels)
	return string(b)
}

// --- building a graph from a model ---

func (e *Engine) buildGraphFromModel(def *graphModelDef) (*Graph, error) {
	g, _, err := e.buildGraphFromModelStats(def)
	return g, err
}

// buildGraphFromModelStats builds the graph and reports each step's
// run: on failure, the steps up to and including the failed one.
func (e *Engine) buildGraphFromModelStats(def *graphModelDef) (*Graph, []graphStepStat, error) {
	var stats []graphStepStat
	g, err := e.buildGraphFromModelInto(def, &stats)
	return g, stats, err
}

func (e *Engine) buildGraphFromModelInto(def *graphModelDef, stats *[]graphStepStat) (*Graph, error) {
	type stepResult struct {
		step graphModelStep
		t    *types.Table
		cols []int // query column → union schema column
		a, b int   // id column (nodes) or source/target columns (edges)
		lab  int   // LabelsColumn index (-1 = none)
	}
	g := &Graph{
		NodeRows:      make(map[string]types.Row),
		Adjacency:     make(map[string][]int),
		NodeIdIdx:     -1,
		withNodeIdIdx: -1,
		hasNodeTable:  true,
		NodeIdName:    "NodeId",
		nodeLabels:    make(map[string][]string),
		isModel:       true,
	}
	var results []stepResult
	idTypes := make(map[types.KQLType]bool)
	for i, st := range def.Definition.Steps {
		started := time.Now().UTC()
		*stats = append(*stats, graphStepStat{Kind: st.Kind, StepNumber: i, Status: "Failed",
			StartTime: started.Format(time.RFC3339Nano)})
		stat := &(*stats)[len(*stats)-1]
		finish := func(status string, added int) {
			stat.Status = status
			stat.Duration = types.FormatValue(int64(time.Since(started)/100), types.TypeTimespan)
			stat.AddedElementsCount = int64(added)
		}
		stmt, err := parser.Parse(st.Query)
		if err != nil {
			finish("Failed", 0)
			return nil, fmt.Errorf("graph model step %d: %w", i+1, err)
		}
		t, err := e.Execute(stmt)
		if err != nil {
			finish("Failed", 0)
			return nil, fmt.Errorf("graph model step %d: %w", i+1, err)
		}
		if t == nil {
			finish("Failed", 0)
			return nil, fmt.Errorf("graph model step %d: query returned no table", i+1)
		}
		finish("Completed", len(t.Rows))
		r := stepResult{step: st, t: t, lab: -1}
		target := &g.NodeSchema
		need := func(c string) (int, error) {
			idx := t.Schema.ColumnIndex(c)
			if idx < 0 {
				return 0, fmt.Errorf("graph model step %d: column %q not found in the query result", i+1, c)
			}
			return idx, nil
		}
		if st.Kind == "AddNodes" {
			if r.a, err = need(st.NodeIdColumn); err != nil {
				return nil, err
			}
			idTypes[t.Schema.Columns[r.a].Type] = true
		} else {
			target = &g.EdgeSchema
			if r.a, err = need(st.SourceColumn); err != nil {
				return nil, err
			}
			if r.b, err = need(st.TargetColumn); err != nil {
				return nil, err
			}
			idTypes[t.Schema.Columns[r.a].Type] = true
			idTypes[t.Schema.Columns[r.b].Type] = true
		}
		if st.LabelsColumn != "" {
			if r.lab, err = need(st.LabelsColumn); err != nil {
				return nil, err
			}
		}
		r.cols = make([]int, len(t.Schema.Columns))
		for c, col := range t.Schema.Columns {
			u := target.ColumnIndex(col.Name)
			if u < 0 {
				u = len(target.Columns)
				target.Columns = append(target.Columns, col)
			}
			r.cols[c] = u
		}
		results = append(results, r)
	}

	// One id type across all steps, else ids compare as strings.
	g.idTypeSet = true
	g.idType = types.TypeString
	if len(idTypes) == 1 {
		for t := range idTypes {
			g.idType = t
		}
	} else {
		g.coercedIds = true
	}
	identity := func(v types.Value) types.Value {
		if g.coercedIds && v != nil {
			return fmt.Sprintf("%v", v)
		}
		return v
	}
	addNode := func(id types.Value) string {
		key := graphKey(id)
		if _, seen := g.NodeRows[key]; !seen {
			g.NodeRows[key] = nil
			g.NodeIDs = append(g.NodeIDs, id)
		}
		return key
	}
	rowLabels := func(r *stepResult, row types.Row) []string {
		labels := append([]string(nil), r.step.Labels...)
		if r.lab < 0 || row[r.lab] == nil {
			return labels
		}
		s := fmt.Sprint(row[r.lab])
		if r.t.Schema.Columns[r.lab].Type == types.TypeDynamic {
			var arr []interface{}
			if json.Unmarshal([]byte(s), &arr) == nil {
				for _, x := range arr {
					if x != nil {
						labels = append(labels, fmt.Sprint(x))
					}
				}
				return labels
			}
			var one string
			if json.Unmarshal([]byte(s), &one) == nil {
				s = one
			}
		}
		if s != "" {
			labels = append(labels, s)
		}
		return labels
	}
	mergeLabels := func(have, add []string) []string {
		for _, l := range add {
			dup := false
			for _, h := range have {
				dup = dup || h == l
			}
			if !dup {
				have = append(have, l)
			}
		}
		return have
	}

	nodeWidth, edgeWidth := len(g.NodeSchema.Columns), len(g.EdgeSchema.Columns)
	for ri := range results {
		r := &results[ri]
		for _, row := range r.t.Rows {
			if r.step.Kind == "AddNodes" {
				id := identity(row[r.a])
				if id == nil {
					continue
				}
				key := addNode(id)
				merged := g.NodeRows[key]
				if merged == nil {
					merged = make(types.Row, nodeWidth)
					g.NodeRows[key] = merged
				}
				for c, v := range row {
					if v != nil { // the step that runs last wins
						merged[r.cols[c]] = v
					}
				}
				g.nodeLabels[key] = mergeLabels(g.nodeLabels[key], rowLabels(r, row))
				continue
			}
			src, tgt := identity(row[r.a]), identity(row[r.b])
			if src == nil || tgt == nil {
				continue
			}
			srcKey := addNode(src)
			addNode(tgt)
			erow := make(types.Row, edgeWidth)
			for c, v := range row {
				erow[r.cols[c]] = v
			}
			g.Edges = append(g.Edges, GraphEdge{Source: src, Target: tgt, Row: erow})
			g.edgeLabels = append(g.edgeLabels, rowLabels(r, row))
			g.Adjacency[srcKey] = append(g.Adjacency[srcKey], len(g.Edges)-1)
		}
	}
	return g, nil
}

// --- snapshot persistence ---

func (e *Engine) writeGraphSnapshot(g *Graph, snapshotId string) (nodesSize, edgesSize int64, err error) {
	nodes := make([]types.Row, 0, len(g.NodeIDs))
	for _, id := range g.NodeIDs {
		key := graphKey(id)
		props := ""
		if row := g.NodeRows[key]; row != nil {
			props = encodeGraphRow(row)
		}
		idJSON, _ := json.Marshal(encodeGraphValue(id))
		nodes = append(nodes, types.Row{snapshotId, string(idJSON), labelsJSON(g.nodeLabels[key]), props})
		nodesSize += int64(len(idJSON) + len(props))
	}
	edges := make([]types.Row, 0, len(g.Edges))
	for i, ed := range g.Edges {
		s, _ := json.Marshal(encodeGraphValue(ed.Source))
		t, _ := json.Marshal(encodeGraphValue(ed.Target))
		props := encodeGraphRow(ed.Row)
		edges = append(edges, types.Row{snapshotId, string(s), string(t), labelsJSON(g.edgeLabels[i]), props})
		edgesSize += int64(len(s) + len(t) + len(props))
	}
	if err := e.appendGraphRows(graphSnapshotNodesTable, nodes); err != nil {
		return 0, 0, err
	}
	return nodesSize, edgesSize, e.appendGraphRows(graphSnapshotEdgesTable, edges)
}

func (e *Engine) loadGraphSnapshot(info *graphSnapshotInfo) (*Graph, error) {
	var lay graphLayout
	if err := json.Unmarshal([]byte(info.Layout), &lay); err != nil {
		return nil, fmt.Errorf("graph snapshot %s.%s: bad layout: %w", info.ModelName, info.Name, err)
	}
	nodeSchema, err := schemaFromLayout(lay.Nodes)
	if err != nil {
		return nil, err
	}
	edgeSchema, err := schemaFromLayout(lay.Edges)
	if err != nil {
		return nil, err
	}
	idType, err := types.ParseType(lay.IdType)
	if err != nil {
		return nil, err
	}
	g := &Graph{
		NodeSchema: nodeSchema, EdgeSchema: edgeSchema,
		NodeRows: make(map[string]types.Row), Adjacency: make(map[string][]int),
		NodeIdIdx: -1, withNodeIdIdx: -1, hasNodeTable: true, NodeIdName: "NodeId",
		nodeLabels: make(map[string][]string), isModel: true,
		idType: idType, idTypeSet: true, coercedIds: false,
	}
	decodeId := func(s string) (types.Value, error) {
		v, err := decodeGraphJSON(s)
		if err != nil {
			return nil, err
		}
		return decodeGraphValue(v, idType), nil
	}
	parseLabels := func(v types.Value) []string {
		var out []string
		if s, ok := v.(string); ok {
			json.Unmarshal([]byte(s), &out) //nolint:errcheck // written by writeGraphSnapshot
		}
		return out
	}
	where := "SnapshotId == " + kqlStringLiteral(info.SnapshotId)
	nt, err := e.readGraphTable(graphSnapshotNodesTable, where)
	if err != nil {
		return nil, err
	}
	if nt != nil {
		ci := nt.Schema.ColumnIndex
		idI, labI, propI := ci("Id"), ci("Labels"), ci("Properties")
		for _, r := range nt.Rows {
			id, err := decodeId(fmt.Sprint(r[idI]))
			if err != nil {
				return nil, err
			}
			key := graphKey(id)
			if _, seen := g.NodeRows[key]; !seen {
				g.NodeIDs = append(g.NodeIDs, id)
			}
			var row types.Row
			if p := fmt.Sprint(r[propI]); r[propI] != nil && p != "" {
				if row, err = decodeGraphRow(p, nodeSchema); err != nil {
					return nil, err
				}
			}
			g.NodeRows[key] = row
			g.nodeLabels[key] = parseLabels(r[labI])
		}
	}
	et, err := e.readGraphTable(graphSnapshotEdgesTable, where)
	if err != nil {
		return nil, err
	}
	if et != nil {
		ci := et.Schema.ColumnIndex
		srcI, tgtI, labI, propI := ci("Source"), ci("Target"), ci("Labels"), ci("Properties")
		for _, r := range et.Rows {
			src, err := decodeId(fmt.Sprint(r[srcI]))
			if err != nil {
				return nil, err
			}
			tgt, err := decodeId(fmt.Sprint(r[tgtI]))
			if err != nil {
				return nil, err
			}
			row, err := decodeGraphRow(fmt.Sprint(r[propI]), edgeSchema)
			if err != nil {
				return nil, err
			}
			for _, id := range []types.Value{src, tgt} {
				if _, seen := g.NodeRows[graphKey(id)]; !seen {
					g.NodeRows[graphKey(id)] = nil
					g.NodeIDs = append(g.NodeIDs, id)
				}
			}
			g.Edges = append(g.Edges, GraphEdge{Source: src, Target: tgt, Row: row})
			g.edgeLabels = append(g.edgeLabels, parseLabels(r[labI]))
			sk := graphKey(src)
			g.Adjacency[sk] = append(g.Adjacency[sk], len(g.Edges)-1)
		}
	}
	return g, nil
}

// --- graph() source ---

// resolveGraphSource evaluates graph("Model" [, "Snapshot"]).
func (e *Engine) resolveGraphSource(call *parser.StoredFunctionCall) (*Graph, error) {
	if len(call.ArgTexts) < 1 || len(call.ArgTexts) > 2 {
		return nil, fmt.Errorf("graph() expects a model name and an optional snapshot name")
	}
	// Arguments: the model name, then optionally a snapshot name (also
	// written snapshot="S") or a bool: true builds a transient graph
	// from the model, false is the latest snapshot.
	var model, snapshot string
	transient := false
	for i, text := range call.ArgTexts {
		text = strings.TrimSpace(text)
		named := false
		if eq := strings.IndexByte(text, '='); eq > 0 && i == 1 &&
			strings.EqualFold(strings.TrimSpace(text[:eq]), "snapshot") && !strings.HasPrefix(text[eq:], "==") {
			text, named = strings.TrimSpace(text[eq+1:]), true
		}
		expr, err := parser.ParseExpr(text)
		if err != nil {
			return nil, fmt.Errorf("graph(): %w", err)
		}
		v, err := evalExpr(e, expr, &types.Schema{}, types.Row{})
		if err != nil {
			return nil, fmt.Errorf("graph(): %w", err)
		}
		switch x := v.(type) {
		case string:
			if i == 0 {
				model = x
			} else {
				snapshot = x
			}
		case bool:
			if i == 0 || named {
				return nil, fmt.Errorf("graph(): expected a string, got %v", x)
			}
			transient = x
		default:
			return nil, fmt.Errorf("graph(): arguments are a model name and a snapshot name or bool")
		}
	}
	latest, err := e.latestGraphModel(model)
	if err != nil {
		return nil, err
	}
	if snapshot != "" {
		info, err := e.findGraphSnapshot(model, snapshot)
		if err != nil {
			return nil, err
		}
		return e.loadGraphSnapshot(info)
	}
	if !transient {
		snaps, err := e.graphSnapshots(model)
		if err != nil {
			return nil, err
		}
		if len(snaps) > 0 {
			pick := &snaps[0]
			for i := range snaps {
				if snaps[i].Created > pick.Created {
					pick = &snaps[i]
				}
			}
			return e.loadGraphSnapshot(pick)
		}
	}
	def, err := parseGraphModelDef(latest.Model)
	if err != nil {
		return nil, err
	}
	return e.buildGraphFromModel(def)
}

// --- commands ---

// graphModelSchema is the result shape of the model commands: Name,
// CreationTime, Id; with details, a snapshot count and the Model; the
// full form (.show graph_models details, .create-or-alter) names the
// count SnapshotsCount and adds AuthorizedPrincipals and
// RetentionPolicy (no principals or retention apply here: [] and {}).
func graphModelSchema(details, full bool) types.Schema {
	cols := []types.Column{
		{Name: "Name", Type: types.TypeString},
		{Name: "CreationTime", Type: types.TypeDatetime},
		{Name: "Id", Type: types.TypeString},
	}
	if details {
		count := "SnapshotCount"
		if full {
			count = "SnapshotsCount"
		}
		cols = append(cols, types.Column{Name: count, Type: types.TypeLong},
			types.Column{Name: "Model", Type: types.TypeDynamic})
	}
	if full {
		cols = append(cols, types.Column{Name: "AuthorizedPrincipals", Type: types.TypeDynamic},
			types.Column{Name: "RetentionPolicy", Type: types.TypeDynamic})
	}
	return types.Schema{Columns: cols}
}

func (e *Engine) applyCreateOrAlterGraphModel(cmd *parser.CreateOrAlterGraphModelCmd) (*types.Table, error) {
	if _, err := parseGraphModelDef(cmd.Payload); err != nil {
		return nil, fmt.Errorf(".create-or-alter graph_model %s: %w", cmd.Name, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(escapeJSONStringControls(cmd.Payload))); err != nil {
		return nil, err
	}
	id := newGUIDString()
	now := time.Now().UTC().UnixNano()
	if err := e.appendGraphRows(graphModelsTable, []types.Row{{cmd.Name, id, compact.String(), false, now}}); err != nil {
		return nil, err
	}
	snaps, err := e.graphSnapshots(cmd.Name)
	if err != nil {
		return nil, err
	}
	out := types.NewTable("", graphModelSchema(true, true))
	out.AddRow(types.Row{cmd.Name, now, id, int64(len(snaps)), compact.String(), "[]", "{}"})
	return out, nil
}

func (e *Engine) applyDropGraphModel(cmd *parser.DropGraphModelCmd) (*types.Table, error) {
	if _, err := e.latestGraphModel(cmd.Name); err != nil {
		return nil, fmt.Errorf(".drop graph_model: %w", err)
	}
	if err := e.dropGraphSnapshots(cmd.Name, ""); err != nil {
		return nil, err
	}
	pols, err := e.graphModelPolicyValues(cmd.Name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixNano()
	var tomb []types.Row
	for p := range pols {
		tomb = append(tomb, types.Row{cmd.Name, p, "", true, now})
	}
	if err := e.appendGraphRows(graphModelPolicies, tomb); err != nil {
		return nil, err
	}
	if err := e.appendGraphRows(graphModelsTable, []types.Row{{cmd.Name, "", "", true, now}}); err != nil {
		return nil, err
	}
	return okResult("OK"), nil
}

func (e *Engine) applyShowGraphModels(cmd *parser.ShowGraphModelsCmd) (*types.Table, error) {
	all, order, err := e.graphModelVersions(cmd.Name)
	if err != nil {
		return nil, err
	}
	if cmd.Name != "" && len(all[cmd.Name]) == 0 {
		return nil, fmt.Errorf("graph model %q not found", cmd.Name)
	}
	full := cmd.Details && cmd.Name == "" // .show graph_models details
	out := types.NewTable("", graphModelSchema(cmd.Details, full))
	if cmd.Name == "" {
		sort.Strings(order) // listings are alphabetical by model name
	}
	for _, n := range order {
		vs := all[n]
		switch {
		case cmd.ShowAll:
		case cmd.VersionId == "":
			vs = vs[len(vs)-1:]
		case cmd.VersionId == "*":
		default:
			var pick []graphModelVersion
			for _, v := range vs {
				if strings.EqualFold(v.Id, cmd.VersionId) {
					pick = append(pick, v)
				}
			}
			if len(pick) == 0 {
				return nil, fmt.Errorf("graph model %q has no version %s", n, cmd.VersionId)
			}
			vs = pick
		}
		var snapCount int64
		if cmd.Details {
			snaps, err := e.graphSnapshots(n)
			if err != nil {
				return nil, err
			}
			snapCount = int64(len(snaps))
		}
		for _, v := range vs {
			row := types.Row{v.Name, v.Created, v.Id}
			if cmd.Details {
				row = append(row, snapCount, v.Model)
			}
			if full {
				row = append(row, "[]", "{}")
			}
			out.AddRow(row)
		}
	}
	return out, nil
}

func snapshotSchema(details bool) types.Schema {
	cols := []types.Column{
		{Name: "Name", Type: types.TypeString},
		{Name: "SnapshotTime", Type: types.TypeDatetime},
		{Name: "ModelName", Type: types.TypeString},
		{Name: "ModelId", Type: types.TypeString},
		{Name: "ModelCreationTime", Type: types.TypeDatetime},
	}
	if details {
		cols = append(cols,
			types.Column{Name: "NodesCount", Type: types.TypeLong},
			types.Column{Name: "EdgesCount", Type: types.TypeLong},
			types.Column{Name: "RetentionPolicy", Type: types.TypeDynamic})
	}
	return types.Schema{Columns: cols}
}

func snapshotRow(s graphSnapshotInfo, details bool) types.Row {
	row := types.Row{s.Name, s.Created, s.ModelName, s.ModelId, s.ModelCreated}
	if details {
		// No retention policy applies: snapshots stay until dropped.
		row = append(row, s.Nodes, s.Edges, "{}")
	}
	return row
}

// applyMakeGraphSnapshot builds the model's latest version and freezes
// it. async runs the same work synchronously (the engine has no
// operation queue) and returns the snapshot id as OperationId.
func (e *Engine) applyMakeGraphSnapshot(cmd *parser.MakeGraphSnapshotCmd) (*types.Table, error) {
	v, err := e.latestGraphModel(cmd.Model)
	if err != nil {
		return nil, fmt.Errorf(".make graph_snapshot: %w", err)
	}
	if _, err := e.findGraphSnapshot(cmd.Model, cmd.Name); err == nil {
		return nil, fmt.Errorf(".make graph_snapshot: snapshot %s.%s already exists", cmd.Model, cmd.Name)
	}
	def, err := parseGraphModelDef(v.Model)
	if err != nil {
		return nil, err
	}
	snapId := newGUIDString()
	started := time.Now().UTC()
	g, steps, err := e.buildGraphFromModelStats(def)
	if err != nil {
		details, _ := json.Marshal(map[string]interface{}{"Steps": steps})
		reason := fmt.Sprintf(".make graph_snapshot '%s' from '%s' command failed: %v", cmd.Name, cmd.Model, err)
		if ferr := e.appendGraphRows(graphSnapshotFailures, []types.Row{{snapId, cmd.Model, v.Id, cmd.Name,
			started.UnixNano(), int64(time.Since(started) / 100), reason, "Permanent", string(details)}}); ferr != nil {
			return nil, fmt.Errorf(".make graph_snapshot %s: %w (and recording the failure: %v)", cmd.Name, err, ferr)
		}
		return nil, fmt.Errorf(".make graph_snapshot %s: %w", cmd.Name, err)
	}
	nodesSize, edgesSize, err := e.writeGraphSnapshot(g, snapId)
	if err != nil {
		return nil, err
	}
	lay, _ := json.Marshal(graphLayout{IdType: g.idType.String(), Nodes: layoutColumns(g.NodeSchema), Edges: layoutColumns(g.EdgeSchema)})
	stats, _ := json.Marshal(graphSnapshotStats{Duration: int64(time.Since(started) / 100), NodesSize: nodesSize, EdgesSize: edgesSize, Steps: steps})
	now := time.Now().UTC().UnixNano()
	info := graphSnapshotInfo{Name: cmd.Name, ModelName: cmd.Model, ModelId: v.Id, SnapshotId: snapId,
		Layout: string(lay), ModelCreated: v.Created, Created: now,
		Nodes: int64(len(g.NodeIDs)), Edges: int64(len(g.Edges)), Statistics: string(stats)}
	if err := e.appendGraphRows(graphSnapshotsTable, []types.Row{{info.Name, info.ModelName, info.ModelId,
		info.ModelCreated, info.SnapshotId, info.Nodes, info.Edges, info.Layout, false, now, info.Statistics}}); err != nil {
		return nil, err
	}
	if cmd.Async {
		out := types.NewTable("", types.Schema{Columns: []types.Column{{Name: "OperationId", Type: types.TypeString}}})
		out.AddRow(types.Row{snapId})
		return out, nil
	}
	out := types.NewTable("", snapshotSchema(true))
	out.AddRow(snapshotRow(info, true))
	return out, nil
}

func (e *Engine) applyShowGraphSnapshots(cmd *parser.ShowGraphSnapshotsCmd) (*types.Table, error) {
	if cmd.Report != "" {
		return e.applyGraphSnapshotReport(cmd)
	}
	if cmd.Snapshot != "" {
		info, err := e.findGraphSnapshot(cmd.Model, cmd.Snapshot)
		if err != nil {
			return nil, err
		}
		out := types.NewTable("", snapshotSchema(cmd.Details))
		out.AddRow(snapshotRow(*info, cmd.Details))
		return out, nil
	}
	if cmd.Model != "*" {
		if _, err := e.latestGraphModel(cmd.Model); err != nil {
			return nil, err
		}
	}
	snaps, err := e.graphSnapshots(cmd.Model)
	if err != nil {
		return nil, err
	}
	out := types.NewTable("", snapshotSchema(false))
	for _, s := range snaps {
		out.AddRow(snapshotRow(s, false))
	}
	return out, nil
}

// dropGraphSnapshots tombstones snapshot name of model ("" = all).
func (e *Engine) dropGraphSnapshots(model, name string) error {
	snaps, err := e.graphSnapshots(model)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	var rows []types.Row
	for _, s := range snaps {
		if name == "" || s.Name == name {
			rows = append(rows, types.Row{s.Name, s.ModelName, s.ModelId, s.ModelCreated, s.SnapshotId,
				s.Nodes, s.Edges, s.Layout, true, now, s.Statistics})
		}
	}
	return e.appendGraphRows(graphSnapshotsTable, rows)
}

func (e *Engine) applyDropGraphSnapshot(cmd *parser.DropGraphSnapshotCmd) (*types.Table, error) {
	if cmd.Snapshot == "" {
		if _, err := e.latestGraphModel(cmd.Model); err != nil {
			return nil, fmt.Errorf(".drop graph_snapshots: %w", err)
		}
		if err := e.dropGraphSnapshots(cmd.Model, ""); err != nil {
			return nil, err
		}
		return okResult("OK"), nil
	}
	info, err := e.findGraphSnapshot(cmd.Model, cmd.Snapshot)
	if err != nil {
		return nil, fmt.Errorf(".drop graph_snapshot: %w", err)
	}
	if err := e.dropGraphSnapshots(cmd.Model, cmd.Snapshot); err != nil {
		return nil, err
	}
	out := types.NewTable("", snapshotSchema(false))
	out.AddRow(snapshotRow(*info, false))
	return out, nil
}

// --- snapshot reports ---

func (e *Engine) databaseName() string {
	if e.Catalog != nil && e.Catalog.Database != nil {
		return e.Catalog.Database.Name
	}
	return ""
}

func (e *Engine) applyGraphSnapshotReport(cmd *parser.ShowGraphSnapshotsCmd) (*types.Table, error) {
	if cmd.Model != "*" {
		if _, err := e.latestGraphModel(cmd.Model); err != nil {
			return nil, err
		}
	}
	if cmd.Report == "failures" {
		return e.graphSnapshotFailureReport(cmd.Model)
	}
	var snaps []graphSnapshotInfo
	if cmd.Snapshot != "" {
		info, err := e.findGraphSnapshot(cmd.Model, cmd.Snapshot)
		if err != nil {
			return nil, err
		}
		snaps = []graphSnapshotInfo{*info}
	} else {
		var err error
		if snaps, err = e.graphSnapshots(cmd.Model); err != nil {
			return nil, err
		}
	}
	db := e.databaseName()
	col := func(n string, t types.KQLType) types.Column { return types.Column{Name: n, Type: t} }
	str, lng, dt, ts, real := types.TypeString, types.TypeLong, types.TypeDatetime, types.TypeTimespan, types.TypeReal

	if cmd.Report == "data statistics" {
		// Storage profile. Element counts and the uncompressed size are
		// known per snapshot; compression and index sizes have no
		// per-snapshot equivalent here (the rows share extents with
		// other snapshots) and are left null.
		out := types.NewTable("", types.Schema{Columns: []types.Column{
			col("DatabaseName", str), col("GraphModelName", str), col("SnapshotName", str), col("EntityKind", str),
			col("TotalExtentSize", lng), col("OriginalSize", lng), col("DataCompressedSize", lng),
			col("IndexSize", lng), col("CompressionRatio", real), col("TotalElementCount", lng)}})
		for _, s := range snaps {
			var st graphSnapshotStats
			json.Unmarshal([]byte(s.Statistics), &st) //nolint:errcheck // absent for snapshots made before statistics existed
			out.AddRow(types.Row{db, s.ModelName, s.Name, "Nodes", nil, st.NodesSize, nil, nil, nil, s.Nodes})
			out.AddRow(types.Row{db, s.ModelName, s.Name, "Edges", nil, st.EdgesSize, nil, nil, nil, s.Edges})
		}
		return out, nil
	}

	// Build statistics. CPU time and peak memory are not measured
	// per command and are left null.
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		col("DatabaseName", str), col("ModelName", str), col("ModelId", str), col("Name", str),
		col("SnapshotTime", dt), col("TotalCpu", ts), col("MemoryPeak", lng), col("Duration", ts),
		col("NodesCount", lng), col("EdgesCount", lng), col("NodesSize", lng), col("EdgesSize", lng),
		col("Details", str)}})
	for _, s := range snaps {
		var st graphSnapshotStats
		var duration types.Value
		details := ""
		if s.Statistics != "" && json.Unmarshal([]byte(s.Statistics), &st) == nil {
			duration = st.Duration
			b, _ := json.Marshal(map[string]interface{}{"Steps": st.Steps})
			details = string(b)
		}
		out.AddRow(types.Row{db, s.ModelName, s.ModelId, s.Name, s.Created, nil, nil, duration,
			s.Nodes, s.Edges, st.NodesSize, st.EdgesSize, details})
	}
	return out, nil
}

func (e *Engine) graphSnapshotFailureReport(model string) (*types.Table, error) {
	col := func(n string, t types.KQLType) types.Column { return types.Column{Name: n, Type: t} }
	str, lng, dt, ts := types.TypeString, types.TypeLong, types.TypeDatetime, types.TypeTimespan
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		col("OperationId", str), col("DatabaseName", str), col("ModelName", str), col("ModelId", str),
		col("Name", str), col("SnapshotTime", dt), col("TotalCpu", ts), col("MemoryPeak", lng),
		col("Duration", ts), col("FailureReason", str), col("FailureKind", str), col("Details", str)}})
	where := ""
	if model != "*" {
		where = "ModelName == " + kqlStringLiteral(model)
	}
	t, err := e.readGraphTable(graphSnapshotFailures, where)
	if err != nil || t == nil {
		return out, err
	}
	ci := t.Schema.ColumnIndex
	db := e.databaseName()
	for _, r := range t.Rows {
		out.AddRow(types.Row{r[ci("OperationId")], db, r[ci("ModelName")], r[ci("ModelId")], r[ci("Name")],
			r[ci("SnapshotTime")], nil, nil, r[ci("Duration")], r[ci("FailureReason")], r[ci("FailureKind")], r[ci("Details")]})
	}
	return out, nil
}

// --- graph model policies ---

// graphPolicyDefaults are the documented defaults shown for a policy
// that was never set (or was deleted). retention and caching are
// accepted and stored but have no effect: snapshots are kept until
// dropped and everything is on local disk.
var graphPolicyDefaults = map[string]string{
	"snapshot_indexing": `{"IndexingLevel":"Default"}`,
	"graph_query":       `{"DefaultSnapshotQueryBehavior":"LatestSnapshot"}`,
	"retention":         "",
	"caching":           "",
}

func validateGraphPolicy(name, value string) (string, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(value), &obj); err != nil {
		return "", fmt.Errorf("policy %s: expected a JSON object: %w", name, err)
	}
	allowed := map[string]map[string][]string{
		"snapshot_indexing": {"IndexingLevel": {"Disabled", "Default", "Best"}},
		"graph_query":       {"DefaultSnapshotQueryBehavior": {"LatestSnapshot", "LatestIndexedSnapshot"}},
	}[name]
	for k, v := range obj {
		if allowed == nil {
			break
		}
		values, ok := allowed[k]
		if !ok {
			return "", fmt.Errorf("policy %s: unknown property %q", name, k)
		}
		sv, _ := v.(string)
		valid := false
		for _, a := range values {
			valid = valid || sv == a
		}
		if !valid {
			return "", fmt.Errorf("policy %s: %s must be one of %s", name, k, strings.Join(values, ", "))
		}
	}
	for k := range allowed {
		if _, ok := obj[k]; !ok {
			return "", fmt.Errorf("policy %s: %s is required", name, k)
		}
	}
	var compact bytes.Buffer
	json.Compact(&compact, []byte(value)) //nolint:errcheck // validated above
	return compact.String(), nil
}

// graphModelPolicyValues returns the live policies of model.
func (e *Engine) graphModelPolicyValues(model string) (map[string]string, error) {
	t, err := e.readGraphTable(graphModelPolicies, "ModelName == "+kqlStringLiteral(model))
	if err != nil || t == nil {
		return map[string]string{}, err
	}
	rows := append([]types.Row(nil), t.Rows...)
	ci := t.Schema.ColumnIndex
	createdI := ci("CreatedAt")
	sort.SliceStable(rows, func(a, b int) bool { return types.ToInt64(rows[a][createdI]) < types.ToInt64(rows[b][createdI]) })
	out := map[string]string{}
	for _, r := range rows {
		name := fmt.Sprint(r[ci("PolicyName")])
		if del, _ := r[ci("Deleted")].(bool); del {
			delete(out, name)
			continue
		}
		out[name] = fmt.Sprint(r[ci("Policy")])
	}
	return out, nil
}

func (e *Engine) applyGraphModelPolicy(cmd *parser.GraphModelPolicyCmd) (*types.Table, error) {
	if _, ok := graphPolicyDefaults[cmd.Policy]; !ok {
		return nil, fmt.Errorf("graph model policy %q is not supported (retention, caching, snapshot_indexing, graph_query)", cmd.Policy)
	}
	var models []string
	if cmd.Model == "*" {
		_, order, err := e.graphModelVersions("")
		if err != nil {
			return nil, err
		}
		models = order
		sort.Strings(models)
	} else {
		if _, err := e.latestGraphModel(cmd.Model); err != nil {
			return nil, err
		}
		models = []string{cmd.Model}
	}
	now := time.Now().UTC().UnixNano()
	switch cmd.Action {
	case "alter":
		value, err := validateGraphPolicy(cmd.Policy, cmd.Value)
		if err != nil {
			return nil, err
		}
		if err := e.appendGraphRows(graphModelPolicies, []types.Row{{cmd.Model, cmd.Policy, value, false, now}}); err != nil {
			return nil, err
		}
	case "delete":
		if err := e.appendGraphRows(graphModelPolicies, []types.Row{{cmd.Model, cmd.Policy, "", true, now}}); err != nil {
			return nil, err
		}
	}
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "PolicyName", Type: types.TypeString}, {Name: "EntityName", Type: types.TypeString},
		{Name: "Policy", Type: types.TypeString}, {Name: "ChildEntities", Type: types.TypeString},
		{Name: "EntityType", Type: types.TypeString}}})
	db := e.databaseName()
	for _, m := range models {
		pols, err := e.graphModelPolicyValues(m)
		if err != nil {
			return nil, err
		}
		value, ok := pols[cmd.Policy]
		if !ok {
			value = graphPolicyDefaults[cmd.Policy]
		}
		var policy types.Value = value
		if value == "" {
			policy = nil
		}
		out.AddRow(types.Row{cmd.Policy, fmt.Sprintf("[%s].[%s]", db, m), policy, nil, "GraphModel"})
	}
	return out, nil
}

// applyShowOperations lists graph snapshot builds as operations: each
// snapshot (its id is the OperationId that .make async returns) as
// Completed, each recorded failure as Failed.
func (e *Engine) applyShowOperations(cmd *parser.ShowOperationsCmd) (*types.Table, error) {
	col := func(n string, t types.KQLType) types.Column { return types.Column{Name: n, Type: t} }
	str, dt, ts := types.TypeString, types.TypeDatetime, types.TypeTimespan
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		col("OperationId", str), col("Operation", str), col("NodeId", str), col("StartedOn", dt),
		col("LastUpdatedOn", dt), col("Duration", ts), col("State", str), col("Status", str), col("Database", str)}})
	want := func(id string) bool {
		if len(cmd.Ids) == 0 {
			return true
		}
		for _, w := range cmd.Ids {
			if strings.EqualFold(w, id) {
				return true
			}
		}
		return false
	}
	db := e.databaseName()
	if t, err := e.readGraphTable(graphSnapshotsTable, "not(Deleted)"); err != nil {
		return nil, err
	} else if t != nil {
		ci := t.Schema.ColumnIndex
		for _, r := range t.Rows {
			id := fmt.Sprint(r[ci("SnapshotId")])
			if !want(id) {
				continue
			}
			created := types.ToInt64(r[ci("CreatedAt")])
			var dur int64
			var st graphSnapshotStats
			if si := ci("Statistics"); si >= 0 && r[si] != nil && json.Unmarshal([]byte(fmt.Sprint(r[si])), &st) == nil {
				dur = st.Duration
			}
			out.AddRow(types.Row{id, "GraphSnapshotMake", "", created - dur*100, created, dur, "Completed", "", db})
		}
	}
	if t, err := e.readGraphTable(graphSnapshotFailures, ""); err != nil {
		return nil, err
	} else if t != nil {
		ci := t.Schema.ColumnIndex
		for _, r := range t.Rows {
			id := fmt.Sprint(r[ci("OperationId")])
			if !want(id) {
				continue
			}
			start := types.ToInt64(r[ci("SnapshotTime")])
			dur := types.ToInt64(r[ci("Duration")])
			out.AddRow(types.Row{id, "GraphSnapshotMake", "", start, start + dur*100, dur, "Failed", r[ci("FailureReason")], db})
		}
	}
	return out, nil
}
