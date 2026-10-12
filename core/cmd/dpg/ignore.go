package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
)

// liveRoleNames returns the names of the roles in objs.
func liveRoleNames(objs []pipeline.IRObject) []string {
	var names []string
	for _, o := range objs {
		if r, ok := o.(*ir.Role); ok {
			names = append(names, r.Name)
		}
	}
	return names
}

// printUndeclaredRoles warns about roles that exist in the database but that
// DPG neither manages nor ignores (RFC Section 11.1). It is a warning, not
// drift: roles are shared by everything on the cluster.
func printUndeclaredRoles(w io.Writer, label string, names []string, color bool) {
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	fmt.Fprintf(w, "%s %s  %s\n  roles not declared: %s; declare them or add them to [ignore] roles\n\n",
		ui.Yellow("warning", color), ui.Dim("[undeclared-role]", color), ui.Cyan(label, color),
		strings.Join(quoted, ", "))
}

// ignoringIntrospector drops objects matching rules from an introspection
// result, so dump neither writes them as source nor records them in the
// snapshot.
type ignoringIntrospector struct {
	inner pipeline.Introspector
	rules *ignore.Rules
}

func (i ignoringIntrospector) Introspect(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	objs, err := i.inner.Introspect(ctx, conn)
	if err != nil {
		return nil, err
	}
	return i.rules.FilterObjects(objs), nil
}
