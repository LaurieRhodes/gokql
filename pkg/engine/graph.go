package engine

import (
	"fmt"
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
	NodeIDs   []types.Value        // deterministic node order (first-seen)
	NodeRows  map[string]types.Row // node key → node row in NodeSchema (nil = edge-only node)
	Adjacency map[string][]int     // node key → outgoing edge indices into Edges

	SourceIdx, TargetIdx int // column indices in EdgeSchema
	NodeIdIdx            int // first node table's id column index in NodeSchema (if hasNodeTable)
	hasNodeTable         bool

	// NodeIdName names the id property of nodes derived from edges (no
	// node table): with_node_id's name, else "NodeId".
	NodeIdName string
	// withNodeIdIdx is the with_node_id column in NodeSchema (-1 = none).
	withNodeIdIdx int

	radjOnce sync.Once
	radj     map[string][]int // node key → incoming edge indices (lazy)

	elemMu      sync.Mutex
	elemSchemas map[elemSchemaKey]*types.Schema
}

// GraphEdge is one directed edge with its full source row for property access.
type GraphEdge struct {
	Source, Target types.Value
	Row            types.Row
}

// reverseAdjacency returns node key → incoming edge indices, built once.
func (g *Graph) reverseAdjacency() map[string][]int {
	g.radjOnce.Do(func() {
		g.radj = make(map[string][]int, len(g.Adjacency))
		for ei, edge := range g.Edges {
			k := graphKey(edge.Target)
			g.radj[k] = append(g.radj[k], ei)
		}
	})
	return g.radj
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
	if row := g.NodeRows[graphKey(id)]; row != nil {
		return row
	}
	row := make(types.Row, len(g.NodeSchema.Columns))
	row[g.NodeIdIdx] = id
	if g.withNodeIdIdx >= 0 {
		row[g.withNodeIdIdx] = id
	}
	return row
}

// graphKey formats a node id value as a map key. Type-tagged via
// typedKey (join.go) so string "1" and long 1 are distinct nodes.
func graphKey(v types.Value) string {
	return typedKey(v)
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
	if err := multiResultNotLast(ops); err != nil {
		return nil, err
	}
	st := pipeState{table: input}
	for i := 0; i < len(ops); i++ {
		if st.graph == nil {
			if ob, isOrder := ops[i].(*parser.OrderByOp); isOrder && i+1 < len(ops) {
				if tk, isTake := ops[i+1].(*parser.TakeOp); isTake {
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
		g, err := e.applyMakeGraph(st.table, o)
		if err != nil {
			return st, err
		}
		return pipeState{graph: g}, nil

	case *parser.GraphToTableOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-to-table: input is not a graph (make-graph must precede it)")
		}
		t, err := applyGraphToTable(st.graph, o)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil

	case *parser.GraphMatchOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-match: input is not a graph (make-graph must precede it)")
		}
		t, err := e.applyGraphMatch(st.graph, o)
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
		Adjacency:     make(map[string][]int),
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
	for _, nt := range op.NodeTables {
		t, err := e.executeQuery(&parser.Query{Source: nt.Table})
		if err != nil {
			return nil, fmt.Errorf("make-graph nodes: %w", err)
		}
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

	addNode := func(id types.Value) string {
		key := graphKey(id)
		if _, seen := g.NodeRows[key]; !seen {
			g.NodeRows[key] = nil
			g.NodeIDs = append(g.NodeIDs, id)
		}
		return key
	}

	for _, row := range edges.Rows {
		src := identity(row[srcIdx])
		tgt := identity(row[tgtIdx])
		if src == nil || tgt == nil {
			continue
		}
		srcKey := addNode(src)
		addNode(tgt)
		g.Edges = append(g.Edges, GraphEdge{Source: src, Target: tgt, Row: row})
		g.Adjacency[srcKey] = append(g.Adjacency[srcKey], len(g.Edges)-1)
	}

	width := len(g.NodeSchema.Columns)
	for _, src := range sources {
		for _, row := range src.table.Rows {
			id := identity(row[src.idIdx])
			if id == nil {
				continue
			}
			key := addNode(id)
			merged := g.NodeRows[key]
			if merged == nil {
				merged = make(types.Row, width)
				g.NodeRows[key] = merged
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

	return g, nil
}

// applyGraphToTable materializes the graph as rows.
//   - edges: original edge rows under the edge schema
//   - nodes with a node table: node-table rows in first-seen node order;
//     edge-only nodes emit their id with nulls for property columns
//   - nodes without a node table: a single NodeId column typed from the
//     edge source column
func applyGraphToTable(g *Graph, op *parser.GraphToTableOp) (*types.Table, error) {
	switch op.Output {
	case "edges":
		result := types.NewTable("", g.EdgeSchema)
		for _, edge := range g.Edges {
			result.AddRow(edge.Row)
		}
		return result, nil

	case "nodes":
		result := types.NewTable("", g.nodeElemSchema())
		for _, id := range g.NodeIDs {
			result.AddRow(g.nodeElemRow(id))
		}
		return result, nil

	default:
		return nil, fmt.Errorf("graph-to-table: unknown output %q", op.Output)
	}
}
