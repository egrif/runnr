package runbook

import (
	"embed"
	"slices"
	"strings"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// BuiltinLibrary is a function library that ships with runnr (std), raw,
// or nil. A library saved under the same name replaces it.
func BuiltinLibrary(name string) []byte {
	b, err := builtinFS.ReadFile("builtin/" + name + ".yaml")
	if err != nil {
		return nil
	}
	return b
}

// BuiltinLibraries are their names.
func BuiltinLibraries() []string {
	es, _ := builtinFS.ReadDir("builtin")
	var out []string
	for _, e := range es {
		out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	slices.Sort(out)
	return out
}
