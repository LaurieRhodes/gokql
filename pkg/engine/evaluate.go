package engine

// evaluate.go — the evaluate plugin-invocation operator, verified
// against real ADX's own evaluate operator docs before implementing:
// "[T |] evaluate [ evaluateParameters ] PluginName ([ PluginArgs ])
// [: OutputSchema]".
//
// Built as a GENERAL dispatch mechanism, not a bag_unpack-only
// special case, per an explicit request to make future plugins (e.g.
// pivot) cheap to add: registering a new one is adding one entry to
// evaluatePlugins below, touching neither the parser nor
// parser.EvaluateOp again.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evaluatePluginFunc is the signature every evaluate plugin
// implements. input is the tabular operand (T in "T | evaluate
// Plugin(...)"); argTexts is each argument's raw, unparsed text
// (parser.EvaluateOp's own doc comment explains why parsing is
// deferred to the plugin itself); outputSchema is the optional
// ": (Name: type, ...)" suffix, nil if none was given.
type evaluatePluginFunc func(e *Engine, input *types.Table, argTexts []string, outputSchema *types.Schema) (*types.Table, error)

// evaluatePlugins is the whole registry. Adding a new plugin is
// exactly one new entry here — see this file's own top comment.
var evaluatePlugins = map[string]evaluatePluginFunc{
	"bag_unpack": applyBagUnpackPlugin,
}

func (e *Engine) applyEvaluate(input *types.Table, op *parser.EvaluateOp) (*types.Table, error) {
	plugin, ok := evaluatePlugins[op.PluginName]
	if !ok {
		names := make([]string, 0, len(evaluatePlugins))
		for n := range evaluatePlugins {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("evaluate: unknown plugin %q — supported: %s", op.PluginName, strings.Join(names, ", "))
	}
	return plugin(e, input, op.ArgTexts, op.OutputSchema)
}

// applyBagUnpackPlugin implements bag_unpack — verified against real
// ADX's own bag_unpack plugin docs before implementing: "unpacks a
// single column of type dynamic, by treating each property bag
// top-level slot as a column." T | evaluate bag_unpack(Column
// [, OutputColumnPrefix] [, columnsConflict] [, ignoredProperties])
// [: OutputSchema].
//
// Column is the first, required, POSITIONAL argument — a bare column
// name, not a general expression (bag_unpack(d), never
// bag_unpack(some_func(d))), matching every real ADX example.
// OutputColumnPrefix is an optional second positional argument (a
// string literal). columnsConflict and ignoredProperties are optional
// NAMED arguments and may appear in either order, or be omitted.
func applyBagUnpackPlugin(e *Engine, input *types.Table, argTexts []string, outputSchema *types.Schema) (*types.Table, error) {
	if len(argTexts) == 0 {
		return nil, fmt.Errorf("bag_unpack: expected at least one argument (the column to unpack)")
	}

	columnArg := strings.TrimSpace(argTexts[0])
	colIdx := input.Schema.ColumnIndex(columnArg)
	if colIdx < 0 {
		return nil, fmt.Errorf("bag_unpack: column %q not found", columnArg)
	}
	if input.Schema.Columns[colIdx].Type != types.TypeDynamic {
		return nil, fmt.Errorf("bag_unpack: column %q must be of type dynamic, got %s", columnArg, input.Schema.Columns[colIdx].Type)
	}

	outputColumnPrefix := ""
	columnsConflict := "error" // real ADX's own documented default
	ignoredProperties := map[string]bool{}

	positionalSeen := 1 // Column itself already consumed
	for _, raw := range argTexts[1:] {
		raw = strings.TrimSpace(raw)
		eqIdx := parser.AssignmentEqIndex(raw)
		if eqIdx < 0 {
			if positionalSeen != 1 {
				return nil, fmt.Errorf("bag_unpack: unexpected positional argument %q", raw)
			}
			val, err := evalConstExpr(raw)
			if err != nil {
				return nil, fmt.Errorf("bag_unpack: OutputColumnPrefix %q: %w", raw, err)
			}
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("bag_unpack: OutputColumnPrefix must be a string literal, got %q", raw)
			}
			outputColumnPrefix = s
			positionalSeen++
			continue
		}

		name := strings.ToLower(strings.TrimSpace(raw[:eqIdx]))
		valText := strings.TrimSpace(raw[eqIdx+1:])
		val, err := evalConstExpr(valText)
		if err != nil {
			return nil, fmt.Errorf("bag_unpack: %s: %w", name, err)
		}

		switch name {
		case "columnsconflict":
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("bag_unpack: columnsConflict must be a string literal")
			}
			switch s {
			case "error", "replace_source", "keep_source":
				columnsConflict = s
			default:
				return nil, fmt.Errorf("bag_unpack: columnsConflict must be one of 'error', 'replace_source', 'keep_source', got %q", s)
			}
		case "ignoredproperties":
			arr, ok := parseJSONArray(val)
			if !ok {
				return nil, fmt.Errorf("bag_unpack: ignoredProperties must be a dynamic array of strings")
			}
			for _, item := range arr {
				if s, ok := item.(string); ok {
					ignoredProperties[s] = true
				}
			}
		default:
			return nil, fmt.Errorf("bag_unpack: unknown argument %q", name)
		}
	}

	// Pass 1: parse every row's bag ONCE (reused in pass 3, never
	// re-parsed), collect the union of top-level keys, and infer each
	// key's KQL type from the ORIGINAL Go JSON value (before
	// jsonToKQLValue's own string-conversion for nested objects/arrays
	// collapses that distinction) — a nested object/array must type as
	// TypeDynamic, not TypeString, even though jsonToKQLValue itself
	// returns a Go string for both (this engine's own on-the-wire
	// dynamic representation, matching every other dynamic column,
	// not a special case invented here).
	parsedBags := make([]map[string]interface{}, len(input.Rows))
	keySeen := map[string]bool{}
	var keyOrder []string
	keyType := map[string]types.KQLType{}

	for i, row := range input.Rows {
		bagVal := row[colIdx]
		if bagVal == nil {
			continue
		}
		obj, ok := parseJSONObject(bagVal)
		if !ok {
			continue // non-object dynamic value in this row — silently skipped, matching real ADX's own tolerant, per-row behavior
		}
		parsedBags[i] = obj
		for k, v := range obj {
			if ignoredProperties[k] {
				continue
			}
			if !keySeen[k] {
				keySeen[k] = true
				keyOrder = append(keyOrder, k)
				keyType[k] = jsonValueKQLType(v)
			}
		}
	}
	sort.Strings(keyOrder) // deterministic regardless of Go's own randomized map iteration order

	// keptAsUnpacked/keepSourceKeys/replaceSourceKeys classify each
	// unpacked key against any existing (non-Column) column of the
	// same output name, resolved via columnsConflict.
	keepSourceKeys := map[string]bool{}    // dropped entirely — existing column's own value wins
	replaceSourceKeys := map[string]bool{} // existing column's slot is reused, refilled per-row from the bag

	newSchema := types.Schema{}
	existingNames := map[string]int{}
	for i, col := range input.Schema.Columns {
		if i == colIdx {
			continue
		}
		existingNames[col.Name] = len(newSchema.Columns)
		newSchema.Columns = append(newSchema.Columns, col)
	}

	var newUnpackedKeys []string // keys that become genuinely NEW columns, in final append order
	for _, k := range keyOrder {
		outName := outputColumnPrefix + k
		if existingIdx, collides := existingNames[outName]; collides {
			switch columnsConflict {
			case "error":
				return nil, fmt.Errorf("bag_unpack: output column %q conflicts with an existing column (columnsConflict defaults to 'error') — pass columnsConflict='replace_source' or 'keep_source' to resolve it", outName)
			case "keep_source":
				keepSourceKeys[k] = true
			case "replace_source":
				replaceSourceKeys[k] = true
				colType := keyType[k]
				if outputSchema != nil {
					if oi := outputSchema.ColumnIndex(outName); oi >= 0 {
						colType = outputSchema.Columns[oi].Type
					}
				}
				newSchema.Columns[existingIdx].Type = colType
			}
			continue
		}
		colType := keyType[k]
		if outputSchema != nil {
			if oi := outputSchema.ColumnIndex(outName); oi >= 0 {
				colType = outputSchema.Columns[oi].Type
			}
		}
		newSchema.Columns = append(newSchema.Columns, types.Column{Name: outName, Type: colType})
		newUnpackedKeys = append(newUnpackedKeys, k)
	}

	// Pass 3: build every output row from the already-parsed bag.
	result := types.NewTable(input.Name, newSchema)
	for i, row := range input.Rows {
		newRow := make(types.Row, len(newSchema.Columns))
		obj := parsedBags[i]

		passIdx := 0
		for i2 := range input.Schema.Columns {
			if i2 == colIdx {
				continue
			}
			newRow[passIdx] = row[i2]
			passIdx++
		}

		for k := range replaceSourceKeys {
			outName := outputColumnPrefix + k
			idx := existingNames[outName]
			var val types.Value
			if obj != nil {
				if raw, present := obj[k]; present {
					val = jsonToKQLValue(raw)
				}
			}
			newRow[idx] = val
		}

		for _, k := range newUnpackedKeys {
			outName := outputColumnPrefix + k
			idx := newSchema.ColumnIndex(outName)
			var val types.Value
			if obj != nil {
				if raw, present := obj[k]; present {
					val = jsonToKQLValue(raw)
				}
			}
			if idx >= 0 {
				newRow[idx] = val
			}
		}

		result.AddRow(newRow)
	}

	return result, nil
}

// jsonValueKQLType infers the correct KQL output type for a raw Go
// JSON value (as produced by encoding/json's own default unmarshaling
// into interface{}) — checked BEFORE jsonToKQLValue's own conversion,
// which collapses a nested object/array into a Go string
// indistinguishably from a genuine string value; only the ORIGINAL
// interface{} type still carries that distinction.
func jsonValueKQLType(v interface{}) types.KQLType {
	switch val := v.(type) {
	case nil:
		return types.TypeDynamic
	case string:
		return types.TypeString
	case bool:
		return types.TypeBool
	case float64:
		if val == float64(int64(val)) {
			return types.TypeLong
		}
		return types.TypeReal
	case map[string]interface{}, []interface{}:
		return types.TypeDynamic
	default:
		return types.TypeDynamic
	}
}

// evalConstExpr parses and evaluates text as a self-contained,
// constant scalar expression (a string/number/bool literal or a
// dynamic(...) literal) with no row context at all — used for
// evaluate plugin arguments, which real ADX documents as always being
// constants, never column references (a plugin's arguments configure
// its behavior once for the whole operator invocation, not per row).
func evalConstExpr(text string) (types.Value, error) {
	expr, err := parser.ParseExpr(text)
	if err != nil {
		return nil, err
	}
	return evalExpr(expr, &types.Schema{}, types.Row{})
}
