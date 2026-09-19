package tools

import (
	"github.com/google/jsonschema-go/jsonschema"
)

/*
portableSchema builds a tool's input schema without nullable type unions.

Go pointer fields — `*int`, `*float64`, `*string` — infer as
`"type": ["null", "integer"]`. That is valid JSON Schema, and the official MCP
Inspector accepts it. But `type` as an *array* is read by several MCP clients as
a single string, and they either drop the constraint or reject the tool
outright. A rejected tool does not appear as an error; it appears as a connector
with no actions, which is indistinguishable from one that never connected.

So each union is rewritten into `anyOf` branches carrying one type each. The
contract is unchanged — `null` is still permitted where it was before — and the
same schema now reads correctly to a client that only understands the string
form.

Note what this deliberately does *not* do: it does not make the properties
optional instead. Absent and `null` are different statements, and this codebase
depends on that distinction everywhere else.
*/
func portableSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		// At init time, with types fixed at compile time. A failure here is a
		// programming error, not a runtime condition to degrade around.
		panic("workout-mcp: building input schema: " + err.Error())
	}
	makePortable(schema)
	return schema
}

// makePortable rewrites nullable unions throughout a schema, in place.
func makePortable(s *jsonschema.Schema) {
	if s == nil {
		return
	}

	if len(s.Types) > 1 {
		branches := make([]*jsonschema.Schema, 0, len(s.Types))
		for _, t := range s.Types {
			branches = append(branches, &jsonschema.Schema{Type: t})
		}
		// Cleared, or the union survives alongside the anyOf and a strict client
		// sees both.
		s.Types = nil
		s.AnyOf = branches
	} else if len(s.Types) == 1 && s.Type == "" {
		// Normalised to the single-string form the same clients expect.
		s.Type = s.Types[0]
		s.Types = nil
	}

	for _, child := range s.Properties {
		makePortable(child)
	}
	makePortable(s.Items)
	makePortable(s.AdditionalProperties)
	for _, child := range s.AnyOf {
		makePortable(child)
	}
	for _, child := range s.OneOf {
		makePortable(child)
	}
	for _, child := range s.AllOf {
		makePortable(child)
	}
	for _, child := range s.Defs {
		makePortable(child)
	}
}
