package engine

import (
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// runtimeReturnTypes corrects scalarReturnTypes (Kusto.Language's
// declared types) where the engine's actual result type differs, as
// the Kusto emulator reports it through getschema: the date-part
// functions are declared int but return long.
var runtimeReturnTypes = map[string]types.KQLType{
	"datepart":      types.TypeLong,
	"datetime_part": types.TypeLong,
	"dayofmonth":    types.TypeLong,
	"dayofyear":     types.TypeLong,
	"getmonth":      types.TypeLong,
	"getyear":       types.TypeLong,
	"hourofday":     types.TypeLong,
	"monthofyear":   types.TypeLong,
	"week_of_year":  types.TypeLong,
	"weekofyear":    types.TypeLong,

	// dynamic results with no declared fixed type
	"dynamic":              types.TypeDynamic,
	"array_shift_left":     types.TypeDynamic,
	"array_shift_right":    types.TypeDynamic,
	"bag_merge":            types.TypeDynamic,
	"bag_remove_keys":      types.TypeDynamic,
	"parse_user_agent":     types.TypeDynamic,
	"parse_xml":            types.TypeDynamic,
	"parse_command_line":   types.TypeDynamic,
	"parse_csv":            types.TypeDynamic,
	"series_stats_dynamic": types.TypeDynamic,
}

// overrideReturnType is the result type of a scalar function whose
// type the declared table gets wrong or omits.
func overrideReturnType(name string) (types.KQLType, bool) {
	if t, ok := runtimeReturnTypes[name]; ok {
		return t, true
	}
	// series_* functions return a dynamic array, except the scalar
	// similarity measures.
	if strings.HasPrefix(name, "series_") && name != "series_cosine_similarity" && name != "series_dot_product" {
		return types.TypeDynamic, true
	}
	return 0, false
}
