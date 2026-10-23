package parser

import (
	"reflect"
	"strings"
)

// IsWrite reports whether running stmt can change a database: every
// management command except the .show forms and .help. Queries never
// write. Used by -readonly.
func IsWrite(stmt Statement) bool {
	switch s := stmt.(type) {
	case nil:
		return false
	case *PipedCommand:
		return IsWrite(s.Inner)
	case *CompoundStatement:
		for _, b := range s.Batch {
			if IsWrite(b) {
				return true
			}
		}
		return IsWrite(s.Final)
	case *UpdatePolicyCmd:
		return s.Action != "show"
	case *GraphModelPolicyCmd:
		return s.Action != "show"
	case *HelpCmd:
		return false
	}
	t := reflect.TypeOf(stmt)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	return strings.HasSuffix(name, "Cmd") && !strings.HasPrefix(name, "Show")
}
