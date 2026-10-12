package resolve

import (
	"fmt"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// Deps are the parts of DPG the engine uses.
type Deps struct {
	Differ pipeline.Differ
	// Render writes an object as DPG source, as dpg dump does.
	Render func(obj pipeline.IRObject) string
	// Compile compiles source files (compiler.CompileWith).
	Compile func(files []string, dir string, opts compiler.Options) ([]pipeline.IRObject, error)
}

// RevertOps returns the migration operations that restore item it's
// declared state in the database.
func (deps Deps) RevertOps(t *Target, it drift.Item) ([]pipeline.DiffOp, error) {
	src := findObject(t.Source, it.Object)
	live := findObject(t.Live, it.Object)
	if src == nil || live == nil {
		return nil, fmt.Errorf("revert needs the object in the source and in the database")
	}
	ops, err := drift.RestoreOps(deps.Differ, src, live, it.Object, it.Property)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("nothing to revert")
	}
	return ops, nil
}

func findObject(objs []pipeline.IRObject, key string) pipeline.IRObject {
	for _, o := range objs {
		if o.QualifiedName() == key {
			return o
		}
	}
	return nil
}
