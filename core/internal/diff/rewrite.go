package diff

import (
	"strings"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// diffEnv holds facts about the whole desired state that classifying one
// object's changes needs.
type diffEnv struct {
	// funcVolatile maps a declared function's name, bare and qualified, to
	// whether any overload of it is VOLATILE (PostgreSQL's default when no
	// volatility is declared).
	funcVolatile map[string]bool
	// checkedDomains holds declared domains, bare and qualified, that carry
	// a NOT NULL or CHECK constraint.
	checkedDomains map[string]bool
}

func newDiffEnv(desired []pipeline.IRObject) *diffEnv {
	env := &diffEnv{funcVolatile: map[string]bool{}, checkedDomains: map[string]bool{}}
	for _, obj := range desired {
		switch o := obj.(type) {
		case *ir.Function:
			v := o.Attrs.Volatility
			volatile := v == "" || strings.EqualFold(v, "VOLATILE")
			for _, k := range []string{o.Name, qualKey(o.Schema, o.Name)} {
				env.funcVolatile[k] = env.funcVolatile[k] || volatile
			}
		case *ir.Type:
			if o.Variant == "DOMAIN" && (o.DomainNotNull || len(o.DomainConstraints) > 0) {
				env.checkedDomains[o.Name] = true
				env.checkedDomains[qualKey(o.Schema, o.Name)] = true
			}
		}
	}
	return env
}

// addColumnRewritesTable reports whether ALTER TABLE ADD COLUMN for col
// makes PostgreSQL rewrite every existing row (RFC Section 17.2): an
// identity, SERIAL or STORED generated column, a default calling a
// volatile function, or a domain type with a NOT NULL or CHECK
// constraint. A constant or stable default is stored in the catalog
// instead, and a VIRTUAL generated column is computed on read.
func (e *diffEnv) addColumnRewritesTable(col *ir.Column) bool {
	switch {
	case col.Identity != nil, col.Serial != nil:
		return true
	case col.Generated != nil:
		return col.Generated.Stored
	case col.Default != nil && e.exprIsVolatile(*col.Default):
		return true
	}
	return e.checkedDomains[qualKey(col.Type.Schema, col.Type.Name)]
}

// exprIsVolatile reports whether expr calls a function that is, or may
// be, VOLATILE. An unqualified name resolves in pg_catalog first, as in
// PostgreSQL. A function that is neither built in nor declared in the
// project has unknown volatility and counts as volatile, as does an
// expression that doesn't parse.
func (e *diffEnv) exprIsVolatile(expr string) bool {
	calls, err := ir.FuncCallNames(expr)
	if err != nil {
		return true
	}
	for _, parts := range calls {
		if len(parts) == 0 {
			continue
		}
		name := parts[len(parts)-1]
		schema := ""
		if len(parts) > 1 {
			schema = parts[len(parts)-2]
		}
		if schema == "" || schema == "pg_catalog" {
			if volatileBuiltinFuncs[name] {
				return true
			}
			if nonVolatileBuiltinFuncs[name] {
				continue
			}
			if schema == "pg_catalog" {
				return true
			}
		}
		volatile, declared := e.funcVolatile[qualKey(schema, name)]
		if !declared || volatile {
			return true
		}
	}
	return false
}
