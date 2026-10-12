package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/pipeline"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

// liveUndeclared splits the objects of a live snapshot that desired neither
// declares nor renames from into roles and tablespaces (reported as
// warnings) and other objects (unmanaged), each as "kind name".
func liveUndeclared(snap *pipeline.Snapshot, desired []pipeline.IRObject) (warn, unmanaged []string) {
	declared := map[string]bool{}
	for _, o := range desired {
		declared[o.QualifiedName()] = true
		if k := diff.RenamedFromKey(o); k != "" {
			declared[k] = true
		}
	}
	for key, raw := range snap.Objects {
		if declared[key] {
			continue
		}
		var so snapshotpkg.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil {
			continue
		}
		kind, _, _, ok := snapshotpkg.Ident(&so)
		if !ok || (kind == "schema" && key == "public") {
			continue
		}
		switch kind {
		case "role", "tablespace":
			warn = append(warn, kind+" "+key)
		default:
			unmanaged = append(unmanaged, kind+" "+key)
		}
	}
	sort.Strings(warn)
	sort.Strings(unmanaged)
	return warn, unmanaged
}

// printUndeclared lists, after a dpg plan --live, the live objects the
// source doesn't declare. They are left alone, never dropped.
func printUndeclared(w io.Writer, label string, warn, unmanaged []string, color bool) {
	if len(warn) > 0 {
		fmt.Fprintf(w, "%s %s  %s\n  not declared: %s; declare them or add them to [ignore]\n\n",
			ui.Yellow("warning", color), ui.Dim("[undeclared]", color), ui.Cyan(label, color), strings.Join(warn, ", "))
	}
	if len(unmanaged) > 0 {
		fmt.Fprintf(w, "%s %s  %s\n  %d object(s) not declared, left alone: %s\n\n",
			ui.Cyan("info", color), ui.Dim("[unmanaged]", color), ui.Cyan(label, color), len(unmanaged), strings.Join(unmanaged, ", "))
	}
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
