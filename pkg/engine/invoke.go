package engine

import (
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// invokeSourceBinding is the synthetic table-binding name applyInvoke
// uses to hand the pipeline's current table to resolveStoredFunction
// as the callee's implicit first (tabular) argument. Not user-facing
// (invoke's own real ADX syntax has no name for its implicit source
// either) -- chosen to be extremely unlikely to collide with a real
// column or table name in practice, not guaranteed unique against a
// pathological one. Safe even so: bindStoredFunctionArgs now resolves
// and captures this binding's value exactly once, synchronously,
// before the callee's own body (which could contain ANOTHER `invoke`
// reusing this same name) ever runs -- see PrecomputedTable's own doc
// comment for why that ordering is what makes reusing one fixed name
// safe across nested invoke calls, rather than needing a fresh unique
// name generated per call.
const invokeSourceBinding = "__invoke_source__"

// applyInvoke implements | invoke FunctionName(args...) — see
// InvokeOp's own doc comment (pkg/parser/ast.go) for exactly what's
// in scope (stored functions only, not an inline `let`-lambda).
//
// Desugars to a stored-function call with the pipeline's current
// table prepended as an implicit first argument: binds `input` into
// e.letContext.Tables under invokeSourceBinding (the same table-
// binding map the `as` operator already reuses, and table-source
// resolution already checks first), then constructs a
// StoredFunctionCall whose first ArgText is that binding's bare name
// — resolveStoredFunction's own existing machinery
// (bindStoredFunctionArgs, PrecomputedTable) resolves and captures it
// correctly, since this binding is set on the CALLER's still-current
// LetContext, exactly the scope that machinery now correctly reads
// from (see PrecomputedTable's own doc comment for the fix that made
// this reliable).
func (e *Engine) applyInvoke(input *types.Table, op *parser.InvokeOp) (*types.Table, error) {
	if e.letContext == nil {
		e.setLetContext(&LetContext{
			Scalars:   make(map[string]types.Value),
			Functions: make(map[string]*parser.FunctionDef),
			Tables:    make(map[string]*types.Table),
		})
	}
	e.letContext.Tables[invokeSourceBinding] = input

	argTexts := make([]string, 0, len(op.Call.ArgTexts)+1)
	argTexts = append(argTexts, invokeSourceBinding)
	argTexts = append(argTexts, op.Call.ArgTexts...)

	call := &parser.StoredFunctionCall{Name: op.Call.Name, ArgTexts: argTexts}
	return e.resolveStoredFunction(call)
}

