package precondition

import (
	"fmt"
	"strings"
)

// Fingerprint is a SQL expression for the md5 of an object's catalog
// state (RFC Section 16.10): NULL when the object doesn't exist, "" when
// the kind isn't fingerprinted. Only what identifies the object's
// definition goes in: names, definitions rebuilt by pg_get_*def, types,
// defaults, options, owners, ACLs, comments; never OIDs or statistics
// (row counts, sizes, the last sequence value).
//
// It is evaluated with search_path set to pg_catalog alone (fpBlock), so
// the rebuilt definitions name every other object with its schema,
// whatever the session's search_path. A function is found through the
// PL/pgSQL variable fp_oid, resolved before (fpResolve).
func Fingerprint(kind, key string) string {
	schema, name, qualified := splitKey(key)
	nl := `E'\n'`
	switch kind {
	case "table", "view", "sequence":
		if !qualified {
			return ""
		}
		parts := []string{
			// The relation itself.
			`concat_ws('|', c.relkind, c.relpersistence, pg_get_userbyid(c.relowner), c.relacl::text,
				c.relrowsecurity, c.relforcerowsecurity, array_to_string(c.reloptions, ','),
				obj_description(c.oid, 'pg_class'), c.relreplident,
				(SELECT t.spcname FROM pg_tablespace t WHERE t.oid = c.reltablespace),
				CASE WHEN c.relkind IN ('p') THEN pg_get_partkeydef(c.oid) END,
				CASE WHEN c.relispartition THEN pg_get_expr(c.relpartbound, c.oid) END,
				CASE WHEN c.relkind IN ('v', 'm') THEN pg_get_viewdef(c.oid) END,
				(SELECT a.amname FROM pg_am a WHERE a.oid = c.relam),
				(SELECT string_agg(i.inhparent::regclass::text, ',' ORDER BY i.inhseqno) FROM pg_inherits i WHERE i.inhrelid = c.oid))`,
			// Columns.
			`(SELECT string_agg(concat_ws('|', a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
				pg_get_expr(d.adbin, d.adrelid), a.attidentity, a.attgenerated, a.attacl::text,
				col_description(c.oid, a.attnum), a.attstorage, a.attcompression,
				(SELECT co.collname FROM pg_collation co WHERE co.oid = a.attcollation AND a.attcollation <> 0 AND co.collname <> 'default'),
				array_to_string(a.attoptions, ','), array_to_string(a.attfdwoptions, ',')), ` + nl + ` ORDER BY a.attnum)
			FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped)`,
			// Constraints, indexes, triggers, policies, rules.
			`(SELECT string_agg(concat_ws('|', k.conname, pg_get_constraintdef(k.oid), obj_description(k.oid, 'pg_constraint')), ` + nl + ` ORDER BY k.conname)
			FROM pg_constraint k WHERE k.conrelid = c.oid)`,
			`(SELECT string_agg(concat_ws('|', pg_get_indexdef(i.indexrelid), obj_description(i.indexrelid, 'pg_class')), ` + nl + ` ORDER BY 1)
			FROM pg_index i WHERE i.indrelid = c.oid)`,
			`(SELECT string_agg(concat_ws('|', t.tgname, pg_get_triggerdef(t.oid), t.tgenabled, obj_description(t.oid, 'pg_trigger')), ` + nl + ` ORDER BY t.tgname)
			FROM pg_trigger t WHERE t.tgrelid = c.oid AND NOT t.tgisinternal)`,
			`(SELECT string_agg(concat_ws('|', p.polname, p.polcmd, p.polpermissive, p.polroles::regrole[]::text,
				pg_get_expr(p.polqual, p.polrelid), pg_get_expr(p.polwithcheck, p.polrelid)), ` + nl + ` ORDER BY p.polname)
			FROM pg_policy p WHERE p.polrelid = c.oid)`,
			// A sequence's definition (not its last value) and its owning column.
			`(SELECT concat_ws('|', format_type(s.seqtypid, NULL), s.seqstart, s.seqincrement, s.seqmax, s.seqmin, s.seqcache, s.seqcycle)
			FROM pg_sequence s WHERE s.seqrelid = c.oid)`,
			`(SELECT string_agg(d.refobjid::regclass::text || '.' || a.attname, ',' ORDER BY 1)
			FROM pg_depend d JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
			WHERE c.relkind = 'S' AND d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('a', 'i'))`,
		}
		return fmt.Sprintf(`(SELECT md5(concat_ws(%s, %s))
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = %s AND c.relname = %s)`, nl, strings.Join(parts, ",\n\t\t"), quoteLit(schema), quoteLit(name))

	case "function", "procedure":
		if !qualified {
			return ""
		}
		// The key is schema.name(argument types as the source wrote them):
		// PostgreSQL resolves it like any signature.
		return fmt.Sprintf(`(SELECT md5(concat_ws(%s, pg_get_functiondef(p.oid), pg_get_userbyid(p.proowner), p.proacl::text,
		obj_description(p.oid, 'pg_proc')))
	FROM pg_proc p WHERE p.oid = fp_oid)`, nl)

	case "type":
		if !qualified {
			return ""
		}
		return fmt.Sprintf(`(SELECT md5(concat_ws(%[1]s, t.typtype, pg_get_userbyid(t.typowner), t.typacl::text,
		obj_description(t.oid, 'pg_type'),
		CASE WHEN t.typtype = 'd' THEN concat_ws('|', format_type(t.typbasetype, t.typtypmod), t.typnotnull, t.typdefault,
			(SELECT co.collname FROM pg_collation co WHERE co.oid = t.typcollation AND t.typcollation <> 0)) END,
		(SELECT string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = t.oid),
		(SELECT string_agg(concat_ws('|', k.conname, pg_get_constraintdef(k.oid), k.convalidated), %[1]s ORDER BY k.conname)
			FROM pg_constraint k WHERE k.contypid = t.oid),
		(SELECT string_agg(concat_ws('|', a.attname, format_type(a.atttypid, a.atttypmod), col_description(a.attrelid, a.attnum)), %[1]s ORDER BY a.attnum)
			FROM pg_attribute a WHERE a.attrelid = t.typrelid AND t.typrelid <> 0 AND a.attnum > 0 AND NOT a.attisdropped),
		(SELECT concat_ws('|', format_type(r.rngsubtype, NULL), (SELECT o.opcname FROM pg_opclass o WHERE o.oid = r.rngsubopc), r.rngcanonical, r.rngsubdiff)
			FROM pg_range r WHERE r.rngtypid = t.oid)))
	FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
	WHERE n.nspname = %[2]s AND t.typname = %[3]s)`, nl, quoteLit(schema), quoteLit(name))

	case "schema":
		return fmt.Sprintf(`(SELECT md5(concat_ws(%s, pg_get_userbyid(s.nspowner), s.nspacl::text, obj_description(s.oid, 'pg_namespace')))
	FROM pg_namespace s WHERE s.nspname = %s)`, nl, quoteLit(key))

	case "role":
		// Never the password.
		return fmt.Sprintf(`(SELECT md5(concat_ws(%[1]s,
		concat_ws('|', r.rolsuper, r.rolinherit, r.rolcreaterole, r.rolcreatedb, r.rolcanlogin, r.rolreplication,
			r.rolbypassrls, r.rolconnlimit, r.rolvaliduntil),
		(SELECT string_agg(concat_ws('|', m.roleid::regrole::text, m.admin_option), ',' ORDER BY 1) FROM pg_auth_members m WHERE m.member = r.oid),
		(SELECT string_agg(array_to_string(s.setconfig, ','), ';' ORDER BY s.setdatabase) FROM pg_db_role_setting s WHERE s.setrole = r.oid),
		shobj_description(r.oid, 'pg_authid')))
	FROM pg_roles r WHERE r.rolname = %[2]s)`, nl, quoteLit(key))

	case "extension":
		return fmt.Sprintf(`(SELECT md5(concat_ws(%s, e.extversion, s.nspname, pg_get_userbyid(e.extowner), obj_description(e.oid, 'pg_extension')))
	FROM pg_extension e JOIN pg_namespace s ON s.oid = e.extnamespace WHERE e.extname = %s)`, nl, quoteLit(key))
	}
	return ""
}

// Fingerprints is where a database records its objects' fingerprints: the
// ledger's schema, and the log (database name or _cluster) of the
// migrations.
type Fingerprints struct {
	Schema, Log string
}

func (f Fingerprints) table() string {
	return `"` + strings.ReplaceAll(f.Schema, `"`, `""`) + `".objects`
}

// fpResolve resolves what Fingerprint needs before search_path changes: a
// function's OID from its signature (whose types may need the session's
// search_path).
func fpResolve(kind, key string) string {
	if kind == "function" || kind == "procedure" {
		return fmt.Sprintf("  fp_oid := to_regprocedure(%s);\n", quoteLit(key))
	}
	return ""
}

// fpBlock is PL/pgSQL setting fp to the object's fingerprint, with
// search_path set to pg_catalog only meanwhile.
func fpBlock(kind, key string) string {
	return fpResolve(kind, key) + fmt.Sprintf(`  sp := current_setting('search_path');
  PERFORM set_config('search_path', 'pg_catalog', true);
  fp := %s;
  PERFORM set_config('search_path', sp, true);
`, Fingerprint(kind, key))
}

const fpDeclare = `  sp text;
  fp text;
  fp_oid oid;
`

// Record upserts the object's current fingerprint (nothing when it doesn't
// exist or isn't fingerprinted).
func (f Fingerprints) Record(kind, key, migration string) string {
	if Fingerprint(kind, key) == "" {
		return ""
	}
	return fmt.Sprintf(`DO $dpg$
DECLARE
%sBEGIN
%s  IF fp IS NOT NULL THEN
    INSERT INTO %s (log, kind, key, fingerprint, pg_major, migration, recorded_at)
    VALUES (%s, %s, %s, fp, current_setting('server_version_num')::int / 10000, %s, now())
    ON CONFLICT (log, kind, key) DO UPDATE SET fingerprint = excluded.fingerprint, pg_major = excluded.pg_major,
      migration = excluded.migration, recorded_at = excluded.recorded_at;
  END IF;
END
$dpg$;`, fpDeclare, fpBlock(kind, key), f.table(), quoteLit(f.Log), quoteLit(kind), quoteLit(key), quoteLit(migration))
}

// Forget removes an object's fingerprint (it was dropped or renamed).
func (f Fingerprints) Forget(kind, key string) string {
	return fmt.Sprintf("DELETE FROM %s WHERE log = %s AND kind = %s AND key = %s;", f.table(), quoteLit(f.Log), quoteLit(kind), quoteLit(key))
}

// Guard raises unless the object is as DPG last saw it: its fingerprint is
// the recorded one, taken on this PostgreSQL major version (DPG-E055); no
// recorded fingerprint is refused too (DPG-E054). SET dpg.allow_drift = on
// lets it pass.
func (f Fingerprints) Guard(kind, key string) string {
	if Fingerprint(kind, key) == "" {
		return ""
	}
	k := strings.ReplaceAll(key, "'", "''")
	return fmt.Sprintf(`DO $dpg$
DECLARE
  r record;
%[7]sBEGIN
  IF coalesce(current_setting('dpg.allow_drift', true), '') = 'on' THEN
    RETURN;
  END IF;
  SELECT fingerprint, pg_major INTO r FROM %[1]s WHERE log = %[2]s AND kind = %[3]s AND key = %[4]s;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'DPG-E054: DPG has no record of %[5]s %[6]s in this database: dpg verify (connected) records it when it has no drift, or SET dpg.allow_drift = on runs the migration anyway';
  END IF;
  IF r.pg_major <> current_setting('server_version_num')::int / 10000 THEN
    RAISE EXCEPTION 'DPG-E054: the record of %[5]s %[6]s was taken on PostgreSQL %%: dpg verify (connected) records it again', r.pg_major;
  END IF;
%[8]s  IF fp IS DISTINCT FROM r.fingerprint THEN
    RAISE EXCEPTION 'DPG-E055: %[5]s %[6]s changed since DPG last saw it: dpg verify shows how';
  END IF;
END
$dpg$;`, f.table(), quoteLit(f.Log), quoteLit(kind), quoteLit(key), kind, k, fpDeclare, fpBlock(kind, key))
}
