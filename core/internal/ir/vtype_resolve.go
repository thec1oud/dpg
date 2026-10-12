package ir

import (
	"fmt"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// ResolveVirtualTypes implements pipeline.IRBuilder.
func (b *Builder) ResolveVirtualTypes(objects []pipeline.IRObject, defaultSchema string) error {
	return ResolveVirtualTypes(objects, defaultSchema)
}

// pgBuiltinTypeNames is every type name PostgreSQL resolves without a schema
// qualifier: the non-array, non-composite types in pg_catalog (identical on
// PostgreSQL 14 through 18) plus the single-word SQL spellings the parser
// maps onto them. A VIRTUAL TYPE may not take any of these names (RFC
// Section 5.6), so a type name in DPG source never means two things.
var pgBuiltinTypeNames = map[string]bool{}

func init() {
	for _, n := range []string{
		"aclitem", "any", "anyarray", "anycompatible", "anycompatiblearray",
		"anycompatiblemultirange", "anycompatiblenonarray", "anycompatiblerange",
		"anyelement", "anyenum", "anymultirange", "anynonarray", "anyrange",
		"bit", "bool", "box", "bpchar", "bytea", "char", "cid", "cidr", "circle",
		"cstring", "date", "datemultirange", "daterange", "event_trigger",
		"fdw_handler", "float4", "float8", "gtsvector", "index_am_handler",
		"inet", "int2", "int4", "int4multirange", "int4range", "int8",
		"int8multirange", "int8range", "internal", "interval", "json", "jsonb",
		"jsonpath", "language_handler", "line", "lseg", "macaddr", "macaddr8",
		"money", "name", "numeric", "nummultirange", "numrange", "oid", "path",
		"pg_brin_bloom_summary", "pg_brin_minmax_multi_summary",
		"pg_ddl_command", "pg_dependencies", "pg_lsn", "pg_mcv_list",
		"pg_ndistinct", "pg_node_tree", "pg_snapshot", "point", "polygon",
		"record", "refcursor", "regclass", "regcollation", "regconfig",
		"regdictionary", "regnamespace", "regoper", "regoperator", "regproc",
		"regprocedure", "regrole", "regtype", "table_am_handler", "text", "tid",
		"time", "timestamp", "timestamptz", "timetz", "trigger", "tsm_handler",
		"tsmultirange", "tsquery", "tsrange", "tstzmultirange", "tstzrange",
		"tsvector", "txid_snapshot", "unknown", "uuid", "varbit", "varchar",
		"void", "xid", "xid8", "xml",
		// SQL spellings and DPG-accepted pseudo-types.
		"bigint", "boolean", "character", "dec", "decimal", "float", "int",
		"integer", "nchar", "real", "smallint",
		"bigserial", "serial", "serial2", "serial4", "serial8", "smallserial",
	} {
		pgBuiltinTypeNames[n] = true
	}
}

// ResolveVirtualTypes resolves every column and composite-attribute type
// that names a VIRTUAL TYPE (RFC Section 5.6). The column's Type becomes
// the virtual type's JSON format (json or jsonb, array dimensions kept) and
// VirtualType records the reference, schema-qualified. Type references
// inside virtual type bodies that name another virtual type are qualified
// the same way. Runs once after Merge, before Sort.
//
// An unqualified reference resolves against the referencing object's own
// schema first, then the database's default schema. A virtual type whose
// name is a PostgreSQL built-in type, or whose qualified name another
// declared type or relation already uses, is DPG-E044.
func ResolveVirtualTypes(objects []pipeline.IRObject, defaultSchema string) error {
	if defaultSchema == "" {
		defaultSchema = "public"
	}
	vtypes := make(map[string]*VirtualType)
	for _, obj := range objects {
		if vt, ok := obj.(*VirtualType); ok {
			vtypes[vt.QualifiedName()] = vt
		}
	}
	if len(vtypes) == 0 {
		return nil
	}

	// Names already taken in a schema's type namespace: every declared
	// type (plus a range's multirange companion) and every relation, since
	// each table, view, materialized view and foreign table has a row type
	// of the same name.
	taken := make(map[string]string)
	for _, obj := range objects {
		switch o := obj.(type) {
		case *Type:
			taken[o.QualifiedName()] = "TYPE"
			if o.MultirangeName != "" {
				taken[qualName(o.Schema, o.MultirangeName)] = "multirange type of RANGE " + o.QualifiedName()
			}
		case *Table:
			taken[o.QualifiedName()] = "TABLE"
		case *View:
			if o.Materialized {
				taken[o.QualifiedName()] = "MATERIALIZED VIEW"
			} else {
				taken[o.QualifiedName()] = "VIEW"
			}
		}
	}
	for _, vt := range vtypes {
		if pgBuiltinTypeNames[vt.Name] {
			return pipeline.ErrorfCode(vt.SrcPos, "DPG-E044",
				"VIRTUAL TYPE %s: %q is a PostgreSQL built-in type name", vt.QualifiedName(), vt.Name)
		}
		if what, ok := taken[vt.QualifiedName()]; ok {
			return pipeline.ErrorfCode(vt.SrcPos, "DPG-E044",
				"VIRTUAL TYPE %s: the name is already used by %s", vt.QualifiedName(), what)
		}
	}

	lookup := func(schema, name, ownSchema string) *VirtualType {
		if schema != "" {
			return vtypes[qualName(schema, name)]
		}
		if vt := vtypes[qualName(ownSchema, name)]; vt != nil {
			return vt
		}
		return vtypes[qualName(defaultSchema, name)]
	}
	resolveCol := func(col *Column, ownSchema string) error {
		vt := lookup(col.Type.Schema, col.Type.Name, ownSchema)
		if vt == nil {
			return nil
		}
		if col.Type.Mods != "" {
			return pipeline.Errorf(col.SrcPos, "column %s: virtual type %s takes no type modifier", col.Name, vt.QualifiedName())
		}
		format := vt.JsonFormat
		if format == "" {
			format = "jsonb"
		}
		col.VirtualType = &TypeRef{Schema: vt.Schema, Name: vt.Name, ArrayDims: col.Type.ArrayDims}
		col.Type = TypeRef{Name: format, ArrayDims: col.Type.ArrayDims}
		return nil
	}

	for _, obj := range objects {
		switch o := obj.(type) {
		case *Table:
			for _, col := range o.Columns {
				if err := resolveCol(col, o.Schema); err != nil {
					return err
				}
			}
		case *Type:
			for _, attr := range o.CompositeAttrs {
				if err := resolveCol(attr, o.Schema); err != nil {
					return err
				}
			}
		case *VirtualType:
			o.Body = qualifyVtypeBody(o.Body, func(r VtypeTypeRef) VtypeTypeRef {
				if vt := lookup(r.Schema, r.Name, o.Schema); vt != nil {
					r.Schema = vt.Schema
					r.Virtual = true
				}
				return r
			})
		}
	}
	return nil
}

// qualifyVtypeBody returns body with fn applied to every type reference.
func qualifyVtypeBody(body VtypeBody, fn func(VtypeTypeRef) VtypeTypeRef) VtypeBody {
	switch b := body.(type) {
	case VtypeTypeRef:
		return fn(b)
	case VtypeComposite:
		fields := make([]VtypeField, len(b.Fields))
		for i, f := range b.Fields {
			fields[i] = VtypeField{Name: f.Name, Type: fn(f.Type)}
		}
		return VtypeComposite{Fields: fields}
	case VtypeUnion:
		members := make([]VtypeBody, len(b.Members))
		for i, m := range b.Members {
			members[i] = qualifyVtypeBody(m, fn)
		}
		return VtypeUnion{Members: members}
	default:
		panic(fmt.Sprintf("qualifyVtypeBody: unexpected body %T", body))
	}
}
