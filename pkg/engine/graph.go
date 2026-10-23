package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Graph is the in-memory result of make-graph: a directed graph whose edges
// carry full edge-table rows and whose nodes optionally carry node-table rows.
// It flows between pipeline operators via pipeState (see applyPipeline) and
// never appears as a *types.Table — the engine's tabular invariant holds for
// every non-graph operator.
type Graph struct {
	EdgeSchema types.Schema
	// NodeSchema is the union of the node tables' columns (first
	// occurrence of a name wins its position and type), plus the
	// with_node_id property when it names no existing column.
	// Zero-value unless a node table was supplied.
	NodeSchema types.Schema

	Edges []GraphEdge

	// Node bookkeeping. Keys are formatted node id values (graphKey).
	// Traversal uses the integer index (graph_index.go) instead.
	NodeIDs  []types.Value        // deterministic node order (first-seen)
	NodeRows map[string]types.Row // node key → node row in NodeSchema (nil = edge-only node)

	SourceIdx, TargetIdx int // column indices in EdgeSchema
	NodeIdIdx            int // first node table's id column index in NodeSchema (if hasNodeTable)
	hasNodeTable         bool

	// NodeIdName names the id property of nodes derived from edges (no
	// node table): with_node_id's name, else "NodeId".
	NodeIdName string
	// withNodeIdIdx is the with_node_id column in NodeSchema (-1 = none).
	withNodeIdIdx int
	// coercedIds: node-table id types disagreed with the edges, so
	// every id was formatted as a string.
	coercedIds bool
	// idType, when idTypeSet, is the node id type of a graph built from
	// a graph model (whose ids come from several step queries).
	idType    types.KQLType
	idTypeSet bool
	// Labels (graph models only; make-graph graphs have none):
	// node key → labels, and per-edge labels parallel to Edges.
	nodeLabels map[string][]string
	edgeLabels [][]string
	// Pre-rendered labels() JSON (GQL views): by node ordinal / edge.
	nodeLabelsJS []string
	edgeLabelsJS []string
	isModel      bool

	idxOnce sync.Once
	index   *graphIndex // traversal index (lazy; shared by derived graphs)

	rowsMu      sync.Mutex
	rowsByOrd   []types.Row // NodeRows by ordinal (lazy)
	labelsByOrd [][]string  // nodeLabels by ordinal (lazy)

	elemMu      sync.Mutex
	elemSchemas map[elemSchemaKey]*types.Schema

	gqlOnce sync.Once
	gqlView *Graph // the graph with labels from the Label property
}

// gqlLabeled returns the graph as GQL sees it: a graph model keeps its
// labels; otherwise each node's and edge's labels are its Label
// property, a string or an array of strings (ADX's GQL convention for
// make-graph graphs). The view shares everything else with g.
func (g *Graph) gqlLabeled() *Graph {
	if g.isModel {
		return g
	}
	g.gqlOnce.Do(func() {
		v := &Graph{
			EdgeSchema: g.EdgeSchema, NodeSchema: g.NodeSchema, Edges: g.Edges, NodeIDs: g.NodeIDs,
			NodeRows: g.NodeRows, index: g.idx(), SourceIdx: g.SourceIdx, TargetIdx: g.TargetIdx,
			NodeIdIdx: g.NodeIdIdx, hasNodeTable: g.hasNodeTable, NodeIdName: g.NodeIdName,
			withNodeIdIdx: g.withNodeIdIdx, coercedIds: g.coercedIds, idType: g.idType, idTypeSet: g.idTypeSet,
		}
		// Label values repeat heavily: parse each distinct one once and
		// share the result (label slices are read-only).
		// Their JSON form, which labels() returns, is rendered once too.
		type parsed struct {
			ls []string
			js string
		}
		seen := map[string]parsed{}
		parse := func(val types.Value) parsed {
			str, ok := val.(string)
			if !ok {
				ls := labelValues(val)
				return parsed{ls, labelsJSON(ls)}
			}
			if p, ok := seen[str]; ok {
				return p
			}
			ls := labelValues(str)
			p := parsed{ls, labelsJSON(ls)}
			seen[str] = p
			return p
		}
		if i := g.NodeSchema.ColumnIndex("Label"); i >= 0 && g.hasNodeTable {
			v.nodeLabels = make(map[string][]string, len(g.NodeRows))
			js := make([]string, len(g.NodeIDs))
			rows := g.nodeRowsByOrd()
			for o, row := range rows {
				js[o] = "[]"
				if row != nil {
					p := parse(row[i])
					js[o] = p.js
				}
			}
			for k, row := range g.NodeRows {
				if row != nil {
					v.nodeLabels[k] = parse(row[i]).ls
				}
			}
			v.nodeLabelsJS = js
		}
		if i := g.EdgeSchema.ColumnIndex("Label"); i >= 0 {
			v.edgeLabels = make([][]string, len(g.Edges))
			v.edgeLabelsJS = make([]string, len(g.Edges))
			for ei, e := range g.Edges {
				p := parse(e.Row[i])
				v.edgeLabels[ei], v.edgeLabelsJS[ei] = p.ls, p.js
			}
		}
		g.gqlView = v
	})
	return g.gqlView
}

// labelValues reads a Label property: a JSON array of strings, or one
// string.
func labelValues(v types.Value) []string {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(s), "[") {
		var arr []interface{}
		if json.Unmarshal([]byte(s), &arr) == nil {
			out := make([]string, 0, len(arr))
			for _, a := range arr {
				if a != nil {
					out = append(out, strings.TrimSpace(fmt.Sprint(a)))
				}
			}
			return out
		}
	}
	return []string{s}
}

// GraphEdge is one directed edge with its full source row for property access.
type GraphEdge struct {
	Source, Target types.Value
	Row            types.Row
}

// derivedNodeIdType is the declared type of the synthetic NodeId column
// when nodes are derived from edge endpoints (no node table). If the
// source and target columns disagree, ids of both types coexist, so the
// column widens to string and derivedNodeIdValue formats accordingly.
func (g *Graph) derivedNodeIdType() types.KQLType {
	st := g.EdgeSchema.Columns[g.SourceIdx].Type
	tt := g.EdgeSchema.Columns[g.TargetIdx].Type
	if st == tt {
		return st
	}
	return types.TypeString
}

// derivedNodeIdValue converts a node id for storage under the derived
// NodeId column: identity normally, formatted string when mixed endpoint
// types forced the column to widen.
func (g *Graph) derivedNodeIdValue(id types.Value) types.Value {
	if g.EdgeSchema.Columns[g.SourceIdx].Type == g.EdgeSchema.Columns[g.TargetIdx].Type {
		return id
	}
	if id == nil {
		return nil
	}
	return fmt.Sprintf("%v", id)
}

// nodeIdType is the type node ids are hashed and compared as.
func (g *Graph) nodeIdType() types.KQLType {
	if g.idTypeSet {
		return g.idType
	}
	if g.coercedIds {
		return types.TypeString
	}
	return g.derivedNodeIdType()
}

// nodeElemSchema is the property schema of a single node: the node
// schema with a node table, else the one derived id column.
func (g *Graph) nodeElemSchema() types.Schema {
	if g.hasNodeTable {
		return g.NodeSchema
	}
	return types.Schema{Columns: []types.Column{{Name: g.NodeIdName, Type: g.derivedNodeIdType()}}}
}

// nodeElemRow is a node's property row under nodeElemSchema. A node that
// appears only as an edge endpoint carries its id (in the id and
// with_node_id columns) and nulls elsewhere.
func (g *Graph) nodeElemRow(id types.Value) types.Row {
	if !g.hasNodeTable {
		return types.Row{g.derivedNodeIdValue(id)}
	}
	if o, ok := g.ordOf(id); ok {
		return g.nodeElemRowOrd(o)
	}
	return g.edgeOnlyNodeRow(id)
}

// edgeOnlyNodeRow is the property row of a node known only from edges.
func (g *Graph) edgeOnlyNodeRow(id types.Value) types.Row {
	row := make(types.Row, len(g.NodeSchema.Columns))
	if g.NodeIdIdx >= 0 {
		row[g.NodeIdIdx] = id
	}
	if g.withNodeIdIdx >= 0 {
		row[g.withNodeIdIdx] = id
	}
	return row
}

// nodeLabelsJSON / edgeLabelsJSON are labels() of a node or edge: a
// dynamic array of strings, empty for make-graph graphs.
func (g *Graph) nodeLabelsJSON(id types.Value) string {
	if g.nodeLabels == nil {
		return "[]"
	}
	if o, ok := g.ordOf(id); ok {
		return g.nodeLabelsJSONOrd(o)
	}
	return labelsJSON(g.nodeLabels[graphKey(id)])
}

func (g *Graph) edgeLabelsJSON(ei int) string {
	if g.edgeLabelsJS != nil {
		return g.edgeLabelsJS[ei]
	}
	if ei >= len(g.edgeLabels) {
		return "[]"
	}
	return labelsJSON(g.edgeLabels[ei])
}

// pipeState carries the intermediate result between pipeline operators.
// Exactly one of table/graph is non-nil: tabular operators consume and
// produce tables, make-graph produces a graph, and graph-* operators
// consume it.
type pipeState struct {
	table *types.Table
	graph *Graph
}

// applyPipeline threads the input table through the operator pipeline,
// handling the graph-typed intermediate state introduced by make-graph.
// All top-level pipelines route through here; applyOperator remains the
// pure table→table dispatch for every non-graph operator. Adjacent
// `sort by ... | take N` pairs fuse into a bounded top-N heap
// (applyTopN) instead of a full sort.
func (e *Engine) applyPipeline(input *types.Table, ops []parser.Operator) (*types.Table, error) {
	return e.applyPipelineState(pipeState{table: input}, ops)
}

// applyGraphPipeline runs ops over a graph source (graph("Model")).
func (e *Engine) applyGraphPipeline(g *Graph, ops []parser.Operator) (*types.Table, error) {
	return e.applyPipelineState(pipeState{graph: g}, ops)
}

func (e *Engine) applyPipelineState(st pipeState, ops []parser.Operator) (*types.Table, error) {
	if err := multiResultNotLast(ops); err != nil {
		return nil, err
	}
	for i := 0; i < len(ops); i++ {
		if st.graph == nil {
			if _, isJoin := ops[i].(*parser.JoinOp); isJoin {
				t, n, err := e.applyJoinPipeline(st.table, ops[i:])
				if err != nil {
					return nil, err
				}
				st = pipeState{table: t}
				i += n - 1
				continue
			}
			if ob, isOrder := ops[i].(*parser.OrderByOp); isOrder && i+1 < len(ops) {
				if tk, isTake := ops[i+1].(*parser.TakeOp); isTake && tk.CountExpr == nil {
					t, err := e.applyTopN(st.table, ob, tk.Count)
					if err != nil {
						return nil, err
					}
					st = pipeState{table: t}
					i++ // consumed the take as well
					continue
				}
			}
		}
		var err error
		st, err = e.applyOperatorState(st, ops[i])
		if err != nil {
			return nil, err
		}
	}
	if st.graph != nil {
		if e.graphResultDepth > 0 {
			return e.wrapGraphResult(st.graph), nil
		}
		return nil, fmt.Errorf("query result is a graph; add graph-to-table (or graph-match) to produce tabular output")
	}
	return st.table, nil
}

func (e *Engine) applyOperatorState(st pipeState, op parser.Operator) (pipeState, error) {
	switch o := op.(type) {
	case *parser.MakeGraphOp:
		if st.graph != nil {
			return st, fmt.Errorf("make-graph: input is already a graph")
		}
		if o.PartitionBy != "" {
			t, err := e.applyMakeGraphPartitioned(st.table, o)
			if err != nil {
				return st, err
			}
			return pipeState{table: t}, nil
		}
		g, err := e.applyMakeGraph(st.table, o)
		if err != nil {
			return st, err
		}
		return pipeState{graph: g}, nil

	case *parser.GraphToTableOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-to-table: input is not a graph (make-graph must precede it)")
		}
		t, err := e.applyGraphToTable(st.graph, o)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil

	case *parser.GraphMarkComponentsOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-mark-components: input is not a graph (make-graph must precede it)")
		}
		g := st.graph.cloneForNodeUpdate()
		markComponents(g, o)
		return pipeState{graph: g}, nil

	case *parser.GraphMatchOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-match: input is not a graph (make-graph must precede it)")
		}
		g := st.graph
		if o.GQLLabels {
			g = g.gqlLabeled()
		}
		t, err := e.applyGraphMatch(g, o)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil

	default:
		if st.graph != nil {
			return st, fmt.Errorf("%T: operator requires tabular input but received a graph (add graph-to-table first)", op)
		}
		t, err := e.applyOperator(st.table, op)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil
	}
}

// applyMakeGraph builds a Graph from the edge table. Edges with a null
// source or target are dropped. Node tables, when named, attach their
// rows as node properties (merged by node id across tables, first
// non-null value per property winning) and isolated nodes (present in a
// node table but absent from any edge) join the node set; name
// resolution against let bindings and the catalog happens inside
// executeQuery, as with lookup.
func (e *Engine) applyMakeGraph(edges *types.Table, op *parser.MakeGraphOp) (*Graph, error) {
	nodeTables, err := e.loadGraphNodeTables(op)
	if err != nil {
		return nil, err
	}
	return buildGraph(edges, op, nodeTables)
}

// loadGraphNodeTables evaluates make-graph's node tables.
func (e *Engine) loadGraphNodeTables(op *parser.MakeGraphOp) ([]*types.Table, error) {
	var out []*types.Table
	for _, nt := range op.NodeTables {
		t, err := e.executeQuery(&parser.Query{Source: nt.Table})
		if err != nil {
			return nil, fmt.Errorf("make-graph nodes: %w", err)
		}
		out = append(out, t)
	}
	return out, nil
}

// applyMakeGraphPartitioned is make-graph ... partitioned-by Col (ops):
// one graph per distinct Col value (edges and node tables split alike),
// ops run on each, results unioned in first-seen partition order.
func (e *Engine) applyMakeGraphPartitioned(edges *types.Table, op *parser.MakeGraphOp) (*types.Table, error) {
	nodeTables, err := e.loadGraphNodeTables(op)
	if err != nil {
		return nil, err
	}
	col := edges.Schema.ColumnIndex(op.PartitionBy)
	if col < 0 {
		return nil, fmt.Errorf("make-graph partitioned-by: column %q not found in the edges", op.PartitionBy)
	}
	nodeCols := make([]int, len(nodeTables))
	for i, t := range nodeTables {
		nodeCols[i] = t.Schema.ColumnIndex(op.PartitionBy)
		if nodeCols[i] < 0 {
			return nil, fmt.Errorf("make-graph partitioned-by: column %q not found in node table %s", op.PartitionBy, op.NodeTables[i].Table)
		}
	}

	var keys []string
	parts := make(map[string]*types.Table)
	for _, row := range edges.Rows {
		k := typedKey(row[col])
		t := parts[k]
		if t == nil {
			t = types.NewTable(edges.Name, edges.Schema)
			parts[k] = t
			keys = append(keys, k)
		}
		t.AddRow(row)
	}
	partNodes := make([]map[string]*types.Table, len(nodeTables))
	for i, nt := range nodeTables {
		partNodes[i] = make(map[string]*types.Table)
		for _, row := range nt.Rows {
			k := typedKey(row[nodeCols[i]])
			t := partNodes[i][k]
			if t == nil {
				t = types.NewTable(nt.Name, nt.Schema)
				partNodes[i][k] = t
			}
			t.AddRow(row)
			if parts[k] == nil { // a partition holding only nodes
				parts[k] = types.NewTable(edges.Name, edges.Schema)
				keys = append(keys, k)
			}
		}
	}

	run := func(edgePart *types.Table, key string, empty bool) (*types.Table, error) {
		nts := make([]*types.Table, len(nodeTables))
		for i, nt := range nodeTables {
			if t := partNodes[i][key]; t != nil && !empty {
				nts[i] = t
			} else {
				nts[i] = types.NewTable(nt.Name, nt.Schema)
			}
		}
		g, err := buildGraph(edgePart, op, nts)
		if err != nil {
			return nil, err
		}
		st := pipeState{graph: g}
		for _, sub := range op.PartitionOps {
			if st, err = e.applyOperatorState(st, sub); err != nil {
				return nil, err
			}
		}
		if st.graph != nil {
			return nil, fmt.Errorf("make-graph partitioned-by: the graph operator must produce a table (end it with graph-match or graph-to-table)")
		}
		return st.table, nil
	}

	if len(keys) == 0 {
		// No partitions: run once on an empty graph for the schema.
		return run(types.NewTable(edges.Name, edges.Schema), "", true)
	}
	var out *types.Table
	for _, k := range keys {
		t, err := run(parts[k], k, false)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = types.NewTable(t.Name, t.Schema)
		} else if len(t.Schema.Columns) != len(out.Schema.Columns) {
			return nil, fmt.Errorf("make-graph partitioned-by: partitions produced different schemas")
		}
		out.Rows = append(out.Rows, t.Rows...)
	}
	return out, nil
}

// buildGraph builds the Graph from the edge table and the evaluated
// node tables (parallel to op.NodeTables).
func buildGraph(edges *types.Table, op *parser.MakeGraphOp, nodeTables []*types.Table) (*Graph, error) {
	srcIdx := edges.Schema.ColumnIndex(op.SourceColumn)
	if srcIdx < 0 {
		return nil, fmt.Errorf("make-graph: source column %q not found", op.SourceColumn)
	}
	tgtIdx := edges.Schema.ColumnIndex(op.TargetColumn)
	if tgtIdx < 0 {
		return nil, fmt.Errorf("make-graph: target column %q not found", op.TargetColumn)
	}

	g := &Graph{
		EdgeSchema:    edges.Schema,
		NodeRows:      make(map[string]types.Row),
		SourceIdx:     srcIdx,
		TargetIdx:     tgtIdx,
		NodeIdName:    "NodeId",
		withNodeIdIdx: -1,
	}
	if op.WithNodeId != "" {
		g.NodeIdName = op.WithNodeId
	}

	// Load the node tables FIRST so their id-column types are known
	// before any node key is computed. That is what lets identity()
	// below coerce consistently across edge endpoints and node-table
	// ids: typedKey keeps long 1 and string "1" distinct (correct for
	// join), so a node table whose id type differs from the edge
	// endpoints would otherwise match no edge at all, silently.
	type nodeSource struct {
		table  *types.Table
		idIdx  int
		colMap []int // table column → NodeSchema column
	}
	var sources []nodeSource
	for i, nt := range op.NodeTables {
		t := nodeTables[i]
		idIdx := t.Schema.ColumnIndex(nt.IdColumn)
		if idIdx < 0 {
			return nil, fmt.Errorf("make-graph: node id column %q not found in %s", nt.IdColumn, nt.Table)
		}
		src := nodeSource{table: t, idIdx: idIdx, colMap: make([]int, len(t.Schema.Columns))}
		for c, col := range t.Schema.Columns {
			u := g.NodeSchema.ColumnIndex(col.Name)
			if u < 0 {
				u = len(g.NodeSchema.Columns)
				g.NodeSchema.Columns = append(g.NodeSchema.Columns, col)
			}
			src.colMap[c] = u
		}
		if len(sources) == 0 {
			g.NodeIdIdx = src.colMap[idIdx]
		}
		sources = append(sources, src)
		g.hasNodeTable = true
	}

	// coerceIdentity: some node table's id type disagrees with the edge
	// endpoint type(s). Ids are then formatted as strings at extraction
	// — the same widen-to-string policy derivedNodeIdType applies to
	// mismatched endpoints — so every downstream graphKey(id) call
	// computes consistent keys without carrying a flag around.
	coerceIdentity := false
	for _, src := range sources {
		if src.table.Schema.Columns[src.idIdx].Type != g.derivedNodeIdType() {
			coerceIdentity = true
		}
	}
	g.coercedIds = coerceIdentity
	identity := func(id types.Value) types.Value {
		if coerceIdentity && id != nil {
			return fmt.Sprintf("%v", id)
		}
		return id
	}

	if g.hasNodeTable && op.WithNodeId != "" {
		g.withNodeIdIdx = g.NodeSchema.ColumnIndex(op.WithNodeId)
		if g.withNodeIdIdx < 0 {
			idType := sources[0].table.Schema.Columns[sources[0].idIdx].Type
			if coerceIdentity {
				idType = types.TypeString
			}
			g.withNodeIdIdx = len(g.NodeSchema.Columns)
			g.NodeSchema.Columns = append(g.NodeSchema.Columns, types.Column{Name: op.WithNodeId, Type: idType})
		}
	}

	// Nodes get first-seen ordinals as they are met; keys are built in
	// a reused buffer, so only a new node allocates one. The traversal
	// index falls out of the build instead of being recomputed.
	ords := make(map[string]int32)
	var ids []types.Value
	var rows []types.Row
	var keyBuf []byte
	addNode := func(id types.Value) int32 {
		keyBuf = appendGraphKey(keyBuf[:0], id)
		if o, seen := ords[string(keyBuf)]; seen {
			return o
		}
		o := int32(len(ids))
		ords[string(keyBuf)] = o
		ids = append(ids, id)
		rows = append(rows, nil)
		return o
	}

	g.Edges = make([]GraphEdge, 0, len(edges.Rows))
	srcOrd := make([]int32, 0, len(edges.Rows))
	dstOrd := make([]int32, 0, len(edges.Rows))
	for _, row := range edges.Rows {
		src := identity(row[srcIdx])
		tgt := identity(row[tgtIdx])
		if src == nil || tgt == nil {
			continue
		}
		srcOrd = append(srcOrd, addNode(src))
		dstOrd = append(dstOrd, addNode(tgt))
		g.Edges = append(g.Edges, GraphEdge{Source: src, Target: tgt, Row: row})
	}

	width := len(g.NodeSchema.Columns)
	var tableOrder []int32
	for _, src := range sources {
		for _, row := range src.table.Rows {
			id := identity(row[src.idIdx])
			if id == nil {
				continue
			}
			o := addNode(id)
			merged := rows[o]
			if merged == nil {
				merged = make(types.Row, width)
				rows[o] = merged
				tableOrder = append(tableOrder, o)
			}
			for c, v := range row {
				if u := src.colMap[c]; merged[u] == nil {
					merged[u] = v
				}
			}
			if g.withNodeIdIdx >= 0 {
				merged[g.withNodeIdIdx] = id
			}
		}
	}

	// Node order, as ADX lists nodes: node-table rows first, then the
	// nodes known only from edges, each in first-seen order. Ordinals
	// are renumbered to that order.
	n := len(ids)
	newOf := make([]int32, n)
	if g.hasNodeTable {
		order := tableOrder
		for o := 0; o < n; o++ {
			if rows[o] == nil {
				order = append(order, int32(o))
			}
		}
		for newO, oldO := range order {
			newOf[oldO] = int32(newO)
		}
	} else {
		for o := range newOf {
			newOf[o] = int32(o)
		}
	}
	g.NodeIDs = make([]types.Value, n)
	byOrd := make([]types.Row, n)
	for o := 0; o < n; o++ {
		g.NodeIDs[newOf[o]] = ids[o]
		byOrd[newOf[o]] = rows[o]
	}
	g.NodeRows = make(map[string]types.Row, n)
	for k, o := range ords {
		g.NodeRows[k] = rows[o]
		ords[k] = newOf[o]
	}
	for ei := range srcOrd {
		srcOrd[ei], dstOrd[ei] = newOf[srcOrd[ei]], newOf[dstOrd[ei]]
	}
	ix := &graphIndex{ords: ords, src: srcOrd, dst: dstOrd}
	ix.outOff, ix.out = csr(n, srcOrd)
	g.index = ix
	g.rowsByOrd = byOrd

	return g, nil
}

// noResultName marks a statement that produces no result table (a
// graph-to-table binding both outputs to names). It is an empty table,
// harmless wherever a table is expected, and is dropped from a batch's
// results.
const noResultName = "\x00no-result"

func noResultTable() *types.Table { return types.NewTable(noResultName, types.Schema{}) }

func isNoResult(t *types.Table) bool { return t != nil && t.Name == noResultName }

// applyGraphToTable materializes the graph as rows.
//   - edges: original edge rows under the edge schema
//   - nodes: node properties (node-table rows, then nodes known only
//     from edges, which carry just their id); without a node table, a
//     single id column (NodeId, or the with_node_id property)
//
// with_node_id / with_source_id / with_target_id prepend long columns of
// hash(node id). An aliased output is bound for the rest of the batch;
// with both outputs nothing is returned.
func (e *Engine) applyGraphToTable(g *Graph, op *parser.GraphToTableOp) (*types.Table, error) {
	hashCol := func(name string) types.Column { return types.Column{Name: name, Type: types.TypeLong} }
	idHash := func(id types.Value) types.Value {
		if id == nil {
			return nil
		}
		return int64(kqlHash64(id, g.nodeIdType()))
	}

	var nodes, edges *types.Table
	if op.Nodes {
		schema := g.nodeElemSchema()
		if op.NodeIdColumn != "" {
			schema.Columns = append([]types.Column{hashCol(op.NodeIdColumn)}, schema.Columns...)
		}
		nodes = types.NewTable(op.NodesAs, schema)
		for o, id := range g.NodeIDs {
			row := g.nodeElemRowOrd(int32(o))
			if op.NodeIdColumn != "" {
				row = append(types.Row{idHash(id)}, row...)
			}
			nodes.AddRow(row)
		}
	}
	if op.Edges {
		var schema types.Schema
		if op.SourceIdColumn != "" {
			schema.Columns = append(schema.Columns, hashCol(op.SourceIdColumn))
		}
		if op.TargetIdColumn != "" {
			schema.Columns = append(schema.Columns, hashCol(op.TargetIdColumn))
		}
		prefix := len(schema.Columns)
		schema.Columns = append(schema.Columns, g.EdgeSchema.Columns...)
		edges = types.NewTable(op.EdgesAs, schema)
		for _, edge := range g.Edges {
			row := edge.Row
			if prefix > 0 {
				row = make(types.Row, 0, len(schema.Columns))
				if op.SourceIdColumn != "" {
					row = append(row, idHash(edge.Source))
				}
				if op.TargetIdColumn != "" {
					row = append(row, idHash(edge.Target))
				}
				row = append(row, edge.Row...)
			}
			edges.AddRow(row)
		}
	}

	bind := func(name string, t *types.Table) {
		if name != "" {
			e.applyAs(t, &parser.AsOp{Name: name}) //nolint:errcheck // never fails
		}
	}
	if nodes != nil && edges != nil {
		bind(op.NodesAs, nodes)
		bind(op.EdgesAs, edges)
		return noResultTable(), nil
	}
	if nodes != nil {
		bind(op.NodesAs, nodes)
		return nodes, nil
	}
	bind(op.EdgesAs, edges)
	return edges, nil
}

// cloneForNodeUpdate copies the graph so node properties can be changed
// without touching the input, which a let binding may share. Edges and
// adjacency are shared read-only; setNodeProperty copies node rows on
// write.
func (g *Graph) cloneForNodeUpdate() *Graph {
	c := &Graph{
		EdgeSchema:    g.EdgeSchema,
		NodeSchema:    types.Schema{Columns: append([]types.Column(nil), g.NodeSchema.Columns...)},
		Edges:         g.Edges,
		NodeIDs:       g.NodeIDs,
		NodeRows:      make(map[string]types.Row, len(g.NodeRows)),
		index:         g.idx(),
		SourceIdx:     g.SourceIdx,
		TargetIdx:     g.TargetIdx,
		NodeIdIdx:     g.NodeIdIdx,
		hasNodeTable:  g.hasNodeTable,
		NodeIdName:    g.NodeIdName,
		withNodeIdIdx: g.withNodeIdIdx,
		coercedIds:    g.coercedIds,
		idType:        g.idType,
		idTypeSet:     g.idTypeSet,
		nodeLabels:    g.nodeLabels,
		edgeLabels:    g.edgeLabels,
		isModel:       g.isModel,
	}
	for k, r := range g.NodeRows {
		c.NodeRows[k] = r
	}
	return c
}

// queryYieldsGraph reports whether q's result is a graph: a graph
// source or make-graph, followed only by graph-to-graph operators.
func (e *Engine) queryYieldsGraph(q *parser.Query) bool {
	graph := false
	if q.SourceFuncCall != nil && q.SourceFuncCall.Name == "graph" {
		graph = true
	} else if _, ok := e.letContext.LookupGraph(q.Source); ok && q.Source != "" {
		graph = true
	}
	for _, op := range q.Operators {
		switch o := op.(type) {
		case *parser.MakeGraphOp:
			graph = o.PartitionBy == ""
		case *parser.GraphMarkComponentsOp:
		default:
			graph = false
		}
	}
	return graph
}

// executeGraphQuery evaluates a query whose result is a graph.
func (e *Engine) executeGraphQuery(q *parser.Query) (*Graph, error) {
	var st pipeState
	ops := q.Operators
	if q.SourceFuncCall != nil && q.SourceFuncCall.Name == "graph" {
		g, err := e.resolveGraphSource(q.SourceFuncCall)
		if err != nil {
			return nil, err
		}
		st.graph = g
	} else if g, ok := e.letContext.LookupGraph(q.Source); ok && q.Source != "" {
		st.graph = g
	} else {
		// The tabular prefix runs through the normal query path.
		k := 0
		for k < len(ops) {
			if _, ok := ops[k].(*parser.MakeGraphOp); ok {
				break
			}
			k++
		}
		t, err := e.executeQuery(&parser.Query{Source: q.Source, SourceFunc: q.SourceFunc, SourceDB: q.SourceDB,
			SourceFuncCall: q.SourceFuncCall, Operators: ops[:k]})
		if err != nil {
			return nil, err
		}
		st.table = t
		ops = ops[k:]
	}
	for _, op := range ops {
		var err error
		if st, err = e.applyOperatorState(st, op); err != nil {
			return nil, err
		}
	}
	if st.graph == nil {
		return nil, fmt.Errorf("expected a graph")
	}
	return st.graph, nil
}

// graphResultName marks the placeholder table a graph result travels
// in out of a function body or let value (see Engine.graphResultDepth).
const graphResultName = "\x00graph-result"

func (e *Engine) wrapGraphResult(g *Graph) *types.Table {
	t := types.NewTable(graphResultName, types.Schema{})
	if e.graphResults == nil {
		e.graphResults = make(map[*types.Table]*Graph)
	}
	e.graphResults[t] = g
	return t
}

// takeGraphResult returns the graph t stands for (nil when t is an
// ordinary table) and forgets it.
func (e *Engine) takeGraphResult(t *types.Table) *Graph {
	if t == nil || t.Name != graphResultName {
		return nil
	}
	g := e.graphResults[t]
	delete(e.graphResults, t)
	return g
}
