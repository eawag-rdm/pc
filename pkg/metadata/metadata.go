// Package metadata provides a platform-agnostic model for data-package
// metadata and a deferred check mechanism.
//
// A collector maps a source document (e.g. a CKAN package_show response) into
// Entities, attaching fields and the checks to run against them. The checks
// are recorded during collection but executed later by RunChecks — the same
// collect-then-check split used for files elsewhere in pc.
package metadata

import (
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// Rule is a named, deferred validator. Fn receives a field's value(s) and
// returns "" on success or a human-readable problem on failure.
type Rule struct {
	Name string
	Fn   func(values []string) string
}

// Entity is the uniform unit of metadata — a "package" or a "resource" (or a
// future kind). It owns its fields and the checks registered against them.
type Entity struct {
	Kind   string              // "package" | "resource"
	Name   string              // human-readable label
	Fields map[string][]string // field name -> values (blanks dropped)
	checks []boundCheck        // run against Fields by RunChecks
}

// boundCheck ties a Rule to one of its entity's fields. A Rule is
// field-agnostic, so the field it targets is recorded alongside it.
type boundCheck struct {
	field string
	rule  Rule
}

// GetValue satisfies structs.Source so metadata findings flow through the
// existing Message pipeline. An Entity contributes no files.
func (e *Entity) GetValue() []structs.File { return nil }

// Field records a field's values and registers zero or more checks against it
// in one atomic call. Blank/whitespace-only values are dropped; "false" and
// "0" are kept. The checks are stored, not run — see RunChecks.
func (e *Entity) Field(name string, values []string, rules ...Rule) {
	var kept []string
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			kept = append(kept, s)
		}
	}
	if len(kept) > 0 {
		e.Fields[name] = kept
	}
	for _, r := range rules {
		e.checks = append(e.checks, boundCheck{field: name, rule: r})
	}
}

// Metadata is the collection of entities for one data package.
type Metadata struct {
	Entities []*Entity
}

// Entity creates an entity of the given kind, registers it on m, and returns
// it so the collector can attach fields and checks.
func (m *Metadata) Entity(kind, name string) *Entity {
	e := &Entity{Kind: kind, Name: name, Fields: map[string][]string{}}
	m.Entities = append(m.Entities, e)
	return e
}
