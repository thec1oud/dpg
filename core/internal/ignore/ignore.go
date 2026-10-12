// Package ignore matches objects against a project's [ignore] patterns
// (RFC Section 3.9): objects that exist in a database but are not managed
// by DPG, such as roles and schemas a hosting platform creates.
package ignore

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// Rules holds name patterns keyed by snapshot object kind. The zero value
// and nil ignore nothing.
type Rules struct {
	byKind map[string][]string
}

// New returns Rules for patterns keyed by snapshot object kind, as returned
// by project's EffectiveIgnore. Patterns are validated when the config is
// loaded.
func New(byKind map[string][]string) *Rules {
	return &Rules{byKind: byKind}
}

// Match reports whether an object of kind, in schema ("" when it is not
// schema-scoped), named name, is ignored, either by a pattern for its kind
// or because its schema is ignored. A pattern containing '.' is matched
// against "schema.name", any other against the name alone.
func (r *Rules) Match(kind, schema, name string) (pattern string, ok bool) {
	if r == nil {
		return "", false
	}
	if schema != "" {
		if p, ok := matchAny(r.byKind["schema"], schema); ok {
			return p, true
		}
	}
	for _, p := range r.byKind[kind] {
		target := name
		if schema != "" && strings.Contains(p, ".") {
			target = schema + "." + name
		}
		if m, _ := path.Match(p, target); m {
			return p, true
		}
	}
	return "", false
}

func matchAny(patterns []string, s string) (string, bool) {
	for _, p := range patterns {
		if m, _ := path.Match(p, s); m {
			return p, true
		}
	}
	return "", false
}

// MatchObject is Match for an IR object.
func (r *Rules) MatchObject(obj pipeline.IRObject) (pattern string, ok bool) {
	if r == nil || len(r.byKind) == 0 {
		return "", false
	}
	kind, schema, name, ok := snapshot.IdentOf(obj)
	if !ok {
		return "", false
	}
	return r.Match(kind, schema, name)
}

// FilterObjects returns objs without the ignored ones.
func (r *Rules) FilterObjects(objs []pipeline.IRObject) []pipeline.IRObject {
	if r == nil || len(r.byKind) == 0 {
		return objs
	}
	out := objs[:0:0]
	for _, o := range objs {
		if _, ok := r.MatchObject(o); !ok {
			out = append(out, o)
		}
	}
	return out
}

// FilterSnapshot removes the ignored entries from snap.
func (r *Rules) FilterSnapshot(snap *pipeline.Snapshot) error {
	if r == nil || len(r.byKind) == 0 || snap == nil {
		return nil
	}
	for key, raw := range snap.Objects {
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil {
			return err
		}
		kind, schema, name, ok := snapshot.Ident(&so)
		if !ok {
			continue
		}
		if _, ignored := r.Match(kind, schema, name); ignored {
			delete(snap.Objects, key)
		}
	}
	return nil
}

// CheckDeclared returns DPG-E045 for every declared object that matches a
// pattern: DPG would try to create it on every run, since it never sees it
// in the database.
func (r *Rules) CheckDeclared(desired []pipeline.IRObject) error {
	var diags pipeline.Diagnostics
	for _, o := range desired {
		if p, ok := r.MatchObject(o); ok {
			kind, _, _, _ := snapshot.IdentOf(o)
			diags = append(diags, pipeline.ErrorfCode(o.Pos(), "DPG-E045",
				"%s %s is declared but matches the [ignore] pattern %q; remove the declaration or the pattern",
				kind, o.QualifiedName(), p))
		}
	}
	if len(diags) == 0 {
		return nil
	}
	return diags
}
