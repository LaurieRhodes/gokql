package engine

import "github.com/LaurieRhodes/gokql/pkg/types"

// ExternalFunctionResolver is a small, additive extension point for a
// host application that embeds this engine as a genuine Go module
// dependency (not a fork) to resolve a tabular function-call source
// (FuncName(arg1, arg2, ...), see parser.StoredFunctionCall) against
// its own, external system instead of this engine's own persisted
// stored-function catalog.
//
// Motivating case: a host application wiring its own domain-specific
// API (e.g. a GraphQL backend) into KQL so a query like
//
//	connectors(first: 100) | where status == "ERROR" | project name, status
//
// resolves connectors(first: 100) through the host's own adapter,
// while everything after the first | runs through this engine's real,
// unmodified operator pipeline — genuinely unified, not two modes
// stitched together. ArgTexts is passed through exactly as the parser
// captured it: raw, unparsed text (e.g. "first: 100"), never forced
// through this engine's own KQL expression grammar — see
// StoredFunctionCall's own doc comment (parser package) for why that
// parsing is deliberately deferred in the first place. This matters
// concretely here: the host's own argument syntax (GraphQL-style
// named arguments, in the motivating case above) is never assumed to
// be valid KQL at all.
//
// ok=false means "not mine" — falls through to this engine's own,
// normal stored-function lookup completely unchanged, so a host can
// resolve only a subset of names externally and let everything else
// (the host's own KQL-defined functions, or "no such stored function"
// for a genuinely unknown name) behave exactly as it always has.
//
// ok=true and err != nil together means "this WAS mine, and MY
// resolution failed" — propagated directly as the query's own error,
// never silently falls through to the normal stored-function lookup
// afterward (which would produce a confusing, unrelated "no such
// stored function" for a name the resolver just claimed as its own).
type ExternalFunctionResolver interface {
	ResolveExternalFunction(name string, argTexts []string) (result *types.Table, ok bool, err error)
}
