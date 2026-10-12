package ir

import (
	"fmt"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// FuncCallNames returns the name parts of every function call in a SQL
// expression (a column DEFAULT, for example), in source order, nested
// calls included: "coalesce(x, my.f(now()))" gives [my f] and [now].
// coalesce and other expression keywords are not function calls.
func FuncCallNames(expr string) ([][]string, error) {
	res, err := pg_query.Parse("SELECT (" + expr + ")")
	if err != nil {
		return nil, err
	}
	if len(res.Stmts) != 1 {
		return nil, fmt.Errorf("expected one expression, got %d statements", len(res.Stmts))
	}
	var names [][]string
	walkFuncCalls(res.Stmts[0].Stmt.ProtoReflect(), &names)
	return names, nil
}

func walkFuncCalls(m protoreflect.Message, names *[][]string) {
	if !m.IsValid() {
		return
	}
	if fc, ok := m.Interface().(*pg_query.FuncCall); ok {
		*names = append(*names, nodeListToNames(fc.Funcname))
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		if fd.IsList() {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				walkFuncCalls(list.Get(i).Message(), names)
			}
			return true
		}
		walkFuncCalls(v.Message(), names)
		return true
	})
}
