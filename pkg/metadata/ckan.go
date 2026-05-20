package metadata

import "strconv"

// CkanMetadataFromJSON maps the "result" object of a CKAN package_show
// response into a Metadata: one "package" entity plus one "resource" entity
// per resource, with the Eawag publication checks registered against them.
//
// This is the pure mapping step — no IO — so it is fully unit-testable. A
// network-bound collector wraps this after fetching and parsing the response.
func CkanMetadataFromJSON(result map[string]any) *Metadata {
	m := &Metadata{}

	pkg := m.Entity("package", str(result, "name"))
	pkg.Field("title", one(str(result, "title")), Required(), TitleFormat())
	pkg.Field("author", strSlice(result, "author"), Required(), AuthorFormat())
	pkg.Field("private", one(str(result, "private")), Required(), Equals("false"))
	pkg.Field("status", one(str(result, "status")), Required(), Equals("complete"))
	pkg.Field("review_level", one(str(result, "review_level")), Required())
	pkg.Field("reviewed_by", one(str(result, "reviewed_by")), Required())
	pkg.Field("usage_contact", one(str(result, "usage_contact")), Required())

	// embargo: when set, the date must already have passed. DateExpired passes
	// when the field is absent (nothing to check). The exact CKAN key is
	// assumed to be "embargo" — confirm against a real embargoed package.
	pkg.Field("embargo", one(str(result, "embargo")), DateExpired())

	for _, r := range objSlice(result, "resources") {
		res := m.Entity("resource", str(r, "name"))
		res.Field("restricted_level", one(str(r, "restricted_level")),
			Required(), Equals("public"))
	}
	return m
}

// one wraps a single scalar value as a slice for Entity.Field. A blank value
// is dropped by Entity.Field, leaving the field unset.
func one(v string) []string { return []string{v} }

// str returns m[key] as a string. Booleans and numbers are stringified so the
// model stays uniformly string-typed; missing/null yields "".
func str(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// strSlice returns m[key] as a []string, keeping only string elements.
func strSlice(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// objSlice returns m[key] as a []map[string]any, keeping only object elements.
func objSlice(m map[string]any, key string) []map[string]any {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if o, ok := item.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}
