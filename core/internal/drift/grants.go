package drift

import (
	"reflect"
	"sort"
	"strings"
)

// allPrivileges is what a declared ALL (an empty privilege list) grants, per
// object kind (RFC Section 11.2). PostgreSQL 17 adds MAINTAIN on tables; a
// live MAINTAIN is covered by ALL but not required by it, so the same lists
// hold for every supported version.
var allPrivileges = map[string][]string{
	"table":       {"DELETE", "INSERT", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"},
	"view":        {"DELETE", "INSERT", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"},
	"column":      {"INSERT", "REFERENCES", "SELECT", "UPDATE"},
	"sequence":    {"SELECT", "UPDATE", "USAGE"},
	"function":    {"EXECUTE"},
	"procedure":   {"EXECUTE"},
	"aggregate":   {"EXECUTE"},
	"schema":      {"CREATE", "USAGE"},
	"type":        {"USAGE"},
	"fdw":         {"USAGE"},
	"server":      {"USAGE"},
	"tablespace":  {"CREATE"},
	"publication": {},
	// Per parameter (PARAMETER PRIVILEGES, PostgreSQL 15+).
	"parameter_privileges": {"ALTER SYSTEM", "SET"},
}

// defaultPrivilegeKind maps a DEFAULT PRIVILEGES object type to the kind
// whose privileges ALL stands for.
var defaultPrivilegeKind = map[string]string{
	"TABLES": "table", "SEQUENCES": "sequence", "FUNCTIONS": "function",
	"ROUTINES": "function", "TYPES": "type", "SCHEMAS": "schema",
}

// defaultPublic is the privilege PostgreSQL grants PUBLIC on a new object
// of the kind. It shows in the live ACL once any explicit grant is made, and
// is neither drift nor a warning unless the source revokes it.
var defaultPublic = map[string]string{
	"function":  "EXECUTE",
	"procedure": "EXECUTE",
	"aggregate": "EXECUTE",
	"type":      "USAGE",
}

// grantTuple is one privilege held by one role; param is the parameter for
// a parameter privilege, else "".
type grantTuple struct {
	priv, param, role string
	withGrant         bool
}

// privLabel is the privilege as shown in an item ID and description.
func (g grantTuple) privLabel() string {
	if g.param != "" {
		return g.priv + " ON PARAMETER " + g.param
	}
	return g.priv
}

func (g grantTuple) String() string {
	s := g.privLabel() + " TO " + g.role
	if g.withGrant {
		s += " WITH GRANT OPTION"
	}
	return s
}

// grantFinding is a grant-level difference.
type grantFinding struct {
	path    string // "" for the object, "column:c" for a column
	tuple   grantTuple
	change  string // "missing", "extra" or "regranted"
	all     bool   // tuple stands for a declared ALL
	allList []string
}

// grantEntries reads a []ir.Grant / []ir.Revocation / []snapshot.SnapGrant
// value generically: each element's Privileges, Roles and WithGrant.
func grantEntries(v reflect.Value) (out []struct {
	privs     []string
	params    []string
	roles     []string
	withGrant bool
}) {
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return nil
	}
	for i := 0; i < v.Len(); i++ {
		e := structOf(v.Index(i))
		if !e.IsValid() {
			continue
		}
		var ent struct {
			privs     []string
			params    []string
			roles     []string
			withGrant bool
		}
		if f := e.FieldByName("Privileges"); f.IsValid() {
			ent.privs = append(ent.privs, f.Interface().([]string)...)
		}
		if f := e.FieldByName("Parameters"); f.IsValid() {
			ent.params = append(ent.params, f.Interface().([]string)...)
		}
		if f := e.FieldByName("Roles"); f.IsValid() {
			ent.roles = append(ent.roles, f.Interface().([]string)...)
		}
		if f := e.FieldByName("WithGrant"); f.IsValid() {
			ent.withGrant = f.Bool()
		}
		out = append(out, ent)
	}
	return out
}

// paramsOf returns an entry's parameters, or [""] for a non-parameter grant.
func paramsOf(params []string) []string {
	if len(params) == 0 {
		return []string{""}
	}
	return params
}

// privKey identifies a privilege on an optional parameter.
func privKey(priv, param string) string {
	return strings.ToUpper(priv) + "\x00" + strings.ToLower(param)
}

func splitPrivKey(k string) (priv, param string) {
	priv, param, _ = strings.Cut(k, "\x00")
	return priv, param
}

// liveHeld maps role → privilege key → whether held WITH GRANT OPTION.
func liveHeld(v reflect.Value) map[string]map[string]bool {
	held := map[string]map[string]bool{}
	for _, e := range grantEntries(v) {
		for _, r := range e.roles {
			if held[r] == nil {
				held[r] = map[string]bool{}
			}
			for _, param := range paramsOf(e.params) {
				for _, p := range e.privs {
					k := privKey(p, param)
					held[r][k] = held[r][k] || e.withGrant
				}
			}
		}
	}
	return held
}

// compareGrants compares the declared grants and revocations of one grant
// holder (an object or a column) with the live grants.
func compareGrants(kind, path string, liveGrants, snapGrants, snapRevs reflect.Value) []grantFinding {
	var out []grantFinding
	held := liveHeld(liveGrants)
	covered := map[string]map[string]bool{} // role → privilege key declared
	cover := func(role, k string) {
		if covered[role] == nil {
			covered[role] = map[string]bool{}
		}
		covered[role][k] = true
	}

	for _, e := range grantEntries(snapGrants) {
		for _, r := range e.roles {
			for _, param := range paramsOf(e.params) {
				if len(e.privs) == 0 {
					// ALL: every privilege of the kind must be held.
					var missing []string
					for _, p := range allPrivileges[kind] {
						if wg, ok := held[r][privKey(p, param)]; !ok || (e.withGrant && !wg) {
							missing = append(missing, p)
						}
					}
					for k := range held[r] {
						if _, kp := splitPrivKey(k); kp == strings.ToLower(param) {
							cover(r, k) // ALL covers every live privilege, MAINTAIN included
						}
					}
					if len(missing) > 0 {
						out = append(out, grantFinding{path: path, change: "missing", all: true, allList: missing,
							tuple: grantTuple{priv: "ALL", param: param, role: r, withGrant: e.withGrant}})
					}
					continue
				}
				for _, p := range e.privs {
					k := privKey(p, param)
					cover(r, k)
					if wg, ok := held[r][k]; !ok || (e.withGrant && !wg) {
						out = append(out, grantFinding{path: path, change: "missing",
							tuple: grantTuple{priv: strings.ToUpper(p), param: param, role: r, withGrant: e.withGrant}})
					}
				}
			}
		}
	}

	revoked := map[string]map[string]bool{}
	for _, e := range grantEntries(snapRevs) {
		for _, r := range e.roles {
			for _, param := range paramsOf(e.params) {
				privs := e.privs
				if len(privs) == 0 {
					privs = allPrivileges[kind]
				}
				for _, p := range privs {
					k := privKey(p, param)
					if revoked[r] == nil {
						revoked[r] = map[string]bool{}
					}
					revoked[r][k] = true
					if wg, ok := held[r][k]; ok {
						out = append(out, grantFinding{path: path, change: "regranted",
							tuple: grantTuple{priv: strings.ToUpper(p), param: param, role: r, withGrant: wg}})
					}
				}
			}
		}
	}

	for r, privs := range held {
		for k, wg := range privs {
			if covered[r][k] || revoked[r][k] {
				continue
			}
			p, param := splitPrivKey(k)
			if r == "PUBLIC" && param == "" && defaultPublic[kind] == p {
				continue
			}
			out = append(out, grantFinding{path: path, change: "extra", tuple: grantTuple{priv: p, param: param, role: r, withGrant: wg}})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.tuple.role != b.tuple.role {
			return a.tuple.role < b.tuple.role
		}
		return a.tuple.privLabel() < b.tuple.privLabel()
	})
	return out
}

// objectGrantFindings compares grants on the object and on each of its
// named sub-objects that carry grants (columns).
func objectGrantFindings(kind string, live, snap reflect.Value) []grantFinding {
	if kind == "default_privileges" {
		if ot := snap.FieldByName("ObjectType"); ot.IsValid() {
			kind = defaultPrivilegeKind[strings.ToUpper(ot.String())]
		}
	}
	var lg reflect.Value
	if live.IsValid() {
		lg = live.FieldByName("Grants")
	}
	out := compareGrants(kind, "", lg, snap.FieldByName("Grants"), snap.FieldByName("Revocations"))
	if !live.IsValid() {
		return out
	}
	lc, sc := live.FieldByName("Columns"), snap.FieldByName("Columns")
	if lc.IsValid() && sc.IsValid() {
		snapCols := map[string]reflect.Value{}
		for i := 0; i < sc.Len(); i++ {
			snapCols[elemName(sc.Index(i))] = structOf(sc.Index(i))
		}
		for i := 0; i < lc.Len(); i++ {
			l := structOf(lc.Index(i))
			s, ok := snapCols[elemName(lc.Index(i))]
			if !ok || !l.IsValid() {
				continue
			}
			out = append(out, compareGrants("column", "column:"+elemName(lc.Index(i)),
				l.FieldByName("Grants"), s.FieldByName("Grants"), s.FieldByName("Revocations"))...)
		}
	}
	return out
}

// copyGrantSlice converts a snapshot grant slice to the live field's type by
// field name, so the masked object carries exactly the declared grants and
// the differ reports no grant changes (grants are compared by
// compareGrants instead).
func copyGrantSlice(dst, src reflect.Value) reflect.Value {
	out := reflect.MakeSlice(dst.Type(), 0, src.Len())
	et := dst.Type().Elem()
	for i := 0; i < src.Len(); i++ {
		s := structOf(src.Index(i))
		e := reflect.New(et).Elem()
		for _, name := range []string{"Privileges", "Parameters", "Roles", "WithGrant", "GrantedBy", "Cascade"} {
			df, sf := e.FieldByName(name), s.FieldByName(name)
			if df.IsValid() && sf.IsValid() && sf.Type().AssignableTo(df.Type()) {
				df.Set(sf)
			}
		}
		out = reflect.Append(out, e)
	}
	return out
}
