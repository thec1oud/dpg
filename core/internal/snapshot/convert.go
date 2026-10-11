package snapshot

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// toSnapNameMaps converts a slice of pipeline.NameMapEntry to the snapshot form.
func toSnapNameMaps(entries []pipeline.NameMapEntry) []SnapNameMapEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]SnapNameMapEntry, len(entries))
	for i, e := range entries {
		if e.IsLiteral {
			out[i] = SnapNameMapEntry{Tool: e.Tool, Name: e.Value}
		} else {
			out[i] = SnapNameMapEntry{Tool: e.Tool, Rule: e.Value}
		}
	}
	return out
}

// toSnapOptions converts an OPTIONS (...) list to its snapshot form.
func toSnapOptions(opts []pipeline.StorageParam) []SnapOptionKV {
	if len(opts) == 0 {
		return nil
	}
	out := make([]SnapOptionKV, len(opts))
	for i, o := range opts {
		out[i] = SnapOptionKV{Key: o.Key, Value: o.Value}
	}
	return out
}

// toSnapOpFamilyMembers converts an OPERATOR FAMILY's loose members (RFC
// Section 14.4) to their snapshot form. FuncArgs is always non-nil (never omitted)
// even for a zero-arg function, distinguishing "explicitly zero arguments"
// from a stale/unpopulated slice the same way OpFamilyMembersStructured
// distinguishes the outer slice being empty from being stale.
func toSnapOpFamilyMembers(members []pipeline.OpFamilyMember) []SnapOpFamilyMember {
	if len(members) == 0 {
		return nil
	}
	out := make([]SnapOpFamilyMember, len(members))
	for i, m := range members {
		out[i] = SnapOpFamilyMember{
			IsFunction: m.IsFunction, Number: m.Number,
			NameSchema: m.Name.Schema, Name: m.Name.Name,
			LeftType: m.LeftType, RightType: m.RightType,
			FuncArgs:         m.FuncArgs,
			OrderBy:          m.OrderBy,
			SortFamilySchema: m.SortFamily.Schema, SortFamilyName: m.SortFamily.Name,
		}
	}
	return out
}

// toSnapSecurityLabels converts a declared/introspected SECURITY LABEL list
// (RFC Section 14.11) to its snapshot form. Order-independent by nature (see
// diffSecurityLabelSet in internal/diff), so unlike toSnapOpFamilyMembers no
// ordering guarantee is implied here.
func toSnapSecurityLabels(labels []pipeline.SecurityLabel) []SnapSecurityLabel {
	if len(labels) == 0 {
		return nil
	}
	out := make([]SnapSecurityLabel, len(labels))
	for i, l := range labels {
		out[i] = SnapSecurityLabel{Provider: l.Provider, Label: l.Label}
	}
	return out
}

// userMappingPasswordKeys mirrors internal/introspect's own copy of the same
// list (which itself mirrors internal/linter's passwordColNames) — kept as
// a local duplicate rather than a cross-package import, following this
// codebase's established pattern for this exact recurring 5-string need
// (see internal/introspect/opaque.go's identical comment).
var userMappingPasswordKeys = []string{"password", "passwd", "pwd", "secret", "passphrase"}

func isUserMappingPasswordKey(key string) bool {
	lower := strings.ToLower(key)
	for _, kw := range userMappingPasswordKeys {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// toSnapNonSensitiveOptions is toSnapOptions for UserMapping specifically:
// password-like keys are skipped entirely (not just redacted) so they
// never enter the structural diff comparison — see
// ir.UserMapping.Options' doc comment.
func toSnapNonSensitiveOptions(opts []pipeline.StorageParam) []SnapOptionKV {
	var out []SnapOptionKV
	for _, o := range opts {
		if isUserMappingPasswordKey(o.Key) {
			continue
		}
		out = append(out, SnapOptionKV{Key: o.Key, Value: o.Value})
	}
	return out
}

// hashBodyStr returns a SHA-256 hex digest of the body string (trimmed).
// Returns "" for empty strings.
func hashBodyStr(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return fmt.Sprintf("%x", sum)
}

// sourceBodyHash hashes a body for change detection, but only when the body was
// derived from source (parsed and deparsed by the compiler). A reconstructed
// body — rebuilt from the live catalog by the introspector — is canonical but
// not byte-identical to arbitrary hand-written source (schema qualification,
// LC_COLLATE/LC_CTYPE vs LOCALE, option ordering), so hashing it would report
// spurious "body changed" drift when it lands on either side of a diff. For
// reconstructed bodies we store "", which the differ's guard treats as "no
// comparison". Offline plan/apply (source vs source) keep a real hash and so
// still detect genuine edits.
func sourceBodyHash(body string, reconstructed bool) string {
	if reconstructed {
		return ""
	}
	return hashBodyStr(body)
}

// baseAlterablePropRe matches one "KEY = value" assignment for each of a
// BASE type's 7 in-place-alterable properties (Section 5.5) inside its raw
// CREATE TYPE options text — see BaseBodyHashInput.
var baseAlterablePropRe = regexp.MustCompile(`(?i)\b(RECEIVE|SEND|TYPMOD_IN|TYPMOD_OUT|ANALYZE|SUBSCRIPT|STORAGE)\s*=\s*[^,()]+`)

// multirangeParamRe matches one MULTIRANGE_TYPE_NAME = name option in a
// range type's option list, with its separating comma.
var multirangeParamRe = regexp.MustCompile(`(?i)\s*,?\s*\bmultirange_type_name\s*=\s*[^,)]+`)

// RangeBodyHashInput returns a RANGE type's body with its
// MULTIRANGE_TYPE_NAME option removed: that name is diffed on its own
// (SnapType.MultirangeName) because changing it is a rename, not a rebuild.
func RangeBodyHashInput(body string) string {
	return multirangeParamRe.ReplaceAllString(body, "")
}

// BaseBodyHashInput strips the 7 in-place-alterable properties' "KEY =
// value" text out of a BASE type's raw Body before hashing it, so a change
// to only one of them (handled instead by a targeted ALTER TYPE ... SET
// (...), see ir.Type.BaseReceive's doc comment) doesn't also trip the
// immutable-property DROP+CREATE comparison. Purely a hash input — never
// rendered as SQL — so leftover punctuation from the removal is harmless.
func BaseBodyHashInput(body string) string {
	return baseAlterablePropRe.ReplaceAllString(body, "")
}

// operatorHintPropRe matches one "KEY = value" assignment for RESTRICT/JOIN/
// COMMUTATOR/NEGATOR (COMMUTATOR/NEGATOR's value may itself be parenthesized,
// OPERATOR(schema.symbol)) or a bare HASHES/MERGES keyword — RFC Section
// 14.3's 6 optimizer-hint properties — inside an Operator's raw CREATE
// OPERATOR options text. Each keyword may be double-quoted in the
// deparsed/canonical form (JOIN in particular: it's a reserved SQL keyword,
// so pg_query's deparser always renders it as "join" — confirmed via a
// direct compile probe), hence the optional `"?` around each keyword. See
// OperatorCoreBodyHashInput.
var operatorHintPropRe = regexp.MustCompile(`(?i)"?\b(?:RESTRICT|JOIN)\b"?\s*=\s*[^,()]+|"?\b(?:COMMUTATOR|NEGATOR)\b"?\s*=\s*(?:OPERATOR\([^)]*\)|[^,()]+)|"?\b(?:HASHES|MERGES)\b"?`)

// OperatorCoreBodyHashInput strips the 6 optimizer-hint properties' text out
// of an Operator's raw Body before hashing it, the same "strip the
// in-place-alterable bits before hashing" technique as BaseBodyHashInput —
// so a hint-only change (handled instead by a targeted
// ALTER OPERATOR ... SET (...), see ir.Operator.Restrict's doc comment)
// doesn't also trip the core-definition DROP+CREATE comparison. Purely a
// hash input — never rendered as SQL — so leftover punctuation from the
// removal is harmless.
func OperatorCoreBodyHashInput(body string) string {
	return operatorHintPropRe.ReplaceAllString(body, "")
}

// Populate converts objects into SnapObjects and stores them in snap.
func Populate(snap *pipeline.Snapshot, objects []pipeline.IRObject) error {
	for _, obj := range objects {
		so := toSnapObject(obj)
		if so == nil {
			continue
		}
		if err := snap.SetObject(obj.QualifiedName(), so); err != nil {
			return err
		}
	}
	return nil
}

func toSnapObject(obj pipeline.IRObject) *SnapObject {
	switch o := obj.(type) {
	case *ir.Table:
		return &SnapObject{Kind: "table", Table: toSnapTable(o)}
	case *ir.View:
		return &SnapObject{Kind: "view", View: toSnapView(o)}
	case *ir.Function:
		return &SnapObject{Kind: "function", Function: toSnapFunction(o)}
	case *ir.Type:
		return &SnapObject{Kind: "type", Type: toSnapType(o)}
	case *ir.Schema:
		return &SnapObject{Kind: "schema", Schema: toSnapSchema(o)}
	case *ir.Extension:
		return &SnapObject{Kind: "extension", Extension: toSnapExtension(o)}
	case *ir.Sequence:
		return &SnapObject{Kind: "sequence", Sequence: toSnapSequence(o)}
	case *ir.Role:
		return &SnapObject{Kind: "role", Role: toSnapRole(o)}
	case *ir.Procedure:
		so := &SnapOpaque{
			Kind: "procedure", Schema: o.Schema, Name: o.Name,
			Args: ir.ArgsKey(o.Args), BodyHash: o.BodyHash, Comment: o.Comment, Deprecated: o.Deprecated,
			NameMaps:                     toSnapNameMaps(o.NameMaps),
			ProcedureDependsOnExtensions: append([]string(nil), o.DependsOnExtensions...),
			ProcedureOwner:               o.Owner,
			ProcedureTransforms:          flattenTypeRefs(o.Attrs.Transforms),
			ProcedureObjFile:             o.Attrs.ObjFile,
			ProcedureLinkSymbol:          o.Attrs.LinkSymbol,
			ProcedureAtomicBody:          o.Attrs.AtomicBody,
			ProcedureSecurityDef:         o.Attrs.SecurityDef,
			ProcedureConfig:              toSnapFuncConfigs(o.Attrs.Config),
		}
		for _, g := range o.Grants {
			so.Grants = append(so.Grants, toSnapGrant(g))
		}
		for _, r := range o.Revocations {
			so.Revocations = append(so.Revocations, toSnapRevocation(r))
		}
		so.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
		return &SnapObject{Kind: "procedure", Opaque: so}
	case *ir.Aggregate:
		so := &SnapOpaque{
			Kind: "aggregate", Schema: o.Schema, Name: o.Name,
			Args: ir.ArgsKey(o.Args), BodyHash: hashBodyStr(o.Body), Comment: o.Comment, Deprecated: o.Deprecated,
			NameMaps: toSnapNameMaps(o.NameMaps),
			// Populated unconditionally (both source-parsed and introspected
			// Aggregates carry a structured Options list) — see
			// SnapOpaque.AggregateOptionsStructured's doc comment.
			AggregateOptionsStructured: true,
			AggregateOptions:           toSnapOptions(o.Options),
			AggregateOwner:             o.Owner,
		}
		for _, g := range o.Grants {
			so.Grants = append(so.Grants, toSnapGrant(g))
		}
		for _, r := range o.Revocations {
			so.Revocations = append(so.Revocations, toSnapRevocation(r))
		}
		so.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
		return &SnapObject{Kind: "aggregate", Opaque: so}
	// The reliable-tier opaque types below can be introspected, in which case
	// their Body is a catalog reconstruction (Reconstructed == true) whose text
	// won't byte-match hand-written source. sourceBodyHash stores a hash only for
	// source-derived bodies, so offline plan/apply still detect edits while
	// verify/plan --live (which involve a reconstruction) skip the comparison.
	case *ir.Tablespace:
		so := &SnapOpaque{
			Kind: "tablespace", Name: o.Name, TablespaceLocation: o.Location, TablespaceOwner: o.Owner,
			TablespaceStorageParams: flattenParams(o.StorageParams),
			BodyHash:                sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			SecurityLabels: toSnapSecurityLabels(o.SecurityLabels),
		}
		for _, g := range o.Grants {
			so.Grants = append(so.Grants, toSnapGrant(g))
		}
		for _, r := range o.Revocations {
			so.Revocations = append(so.Revocations, toSnapRevocation(r))
		}
		return &SnapObject{Kind: "tablespace", Opaque: so}
	case *ir.ForeignDataWrapper:
		so := &SnapOpaque{
			Kind: "fdw", Name: o.Name,
			OptionsStructured: true, FDWHandler: o.Handler, FDWValidator: o.Validator, FDWOptions: toSnapOptions(o.Options),
			FDWOwner: o.Owner,
			BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
		}
		for _, g := range o.Grants {
			so.Grants = append(so.Grants, toSnapGrant(g))
		}
		for _, r := range o.Revocations {
			so.Revocations = append(so.Revocations, toSnapRevocation(r))
		}
		return &SnapObject{Kind: "fdw", Opaque: so}
	case *ir.ForeignServer:
		so := &SnapOpaque{
			Kind: "server", Name: o.Name,
			OptionsStructured: true, ServerFDWName: o.FDWName, ServerType: o.Type, ServerVersion: o.Version, ServerOptions: toSnapOptions(o.Options),
			ServerOwner: o.Owner,
			BodyHash:    sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
		}
		for _, g := range o.Grants {
			so.Grants = append(so.Grants, toSnapGrant(g))
		}
		for _, r := range o.Revocations {
			so.Revocations = append(so.Revocations, toSnapRevocation(r))
		}
		return &SnapObject{Kind: "server", Opaque: so}
	case *ir.UserMapping:
		return &SnapObject{Kind: "user_mapping", Opaque: &SnapOpaque{
			Kind: "user_mapping", Name: o.User + "@" + o.Server,
			OptionsStructured: true, UserMappingOptions: toSnapNonSensitiveOptions(o.Options),
			BodyHash: sourceBodyHash(o.Body, o.Reconstructed),
		}}
	case *ir.Publication:
		tables := make([]string, len(o.Tables))
		for i, t := range o.Tables {
			if t.Schema != "" {
				tables[i] = t.Schema + "." + t.Name
			} else {
				tables[i] = t.Name
			}
		}
		return &SnapObject{Kind: "publication", Opaque: &SnapOpaque{
			Kind: "publication", Name: o.Name,
			PublicationOwner:      o.Owner,
			PublicationStructured: true, PublicationAllTables: o.AllTables, PublicationTables: tables,
			PublicationInsert: o.Insert, PublicationUpdate: o.Update, PublicationDelete: o.Delete, PublicationTruncate: o.Truncate,
			PublicationHasFilteredTables: o.HasFilteredTables,
			BodyHash:                     sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			SecurityLabels: toSnapSecurityLabels(o.SecurityLabels),
		}}
	case *ir.Subscription:
		return &SnapObject{Kind: "subscription", Opaque: &SnapOpaque{
			Kind: "subscription", Name: o.Name, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			SubscriptionOwner: o.Owner,
			SecurityLabels:    toSnapSecurityLabels(o.SecurityLabels),
		}}
	case *ir.EventTrigger:
		return &SnapObject{Kind: "event_trigger", Opaque: &SnapOpaque{
			Kind: "event_trigger", Name: o.Name,
			EventTriggerEvent: o.Event, EventTriggerTags: o.Tags, EventTriggerFunction: o.Function,
			EventTriggerOwner: o.Owner, EventTriggerEnableState: o.EnableState,
			BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			SecurityLabels: toSnapSecurityLabels(o.SecurityLabels),
		}}
	case *ir.Collation:
		return &SnapObject{Kind: "collation", Opaque: &SnapOpaque{
			Kind: "collation", Schema: o.Schema, Name: o.Name,
			CollationStructured: true, CollationProvider: o.Provider,
			CollationCollate: o.Collate, CollationCtype: o.Ctype, CollationICULocale: o.ICULocale,
			CollationDeterministic: o.Deterministic, CollationOwner: o.Owner, CollationRules: o.Rules,
			BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
		}}
	case *ir.Operator:
		return &SnapObject{Kind: "operator", Opaque: &SnapOpaque{
			Kind: "operator", Schema: o.Schema, Name: o.Name,
			Args:               ir.OperandsKey(o.LeftType, o.RightType),
			BodyHash:           sourceBodyHash(o.Body, o.Reconstructed),
			Comment:            o.Comment,
			OperatorStructured: true,
			OperatorRestrict:   o.Restrict,
			OperatorJoin:       o.Join,
			OperatorCommutator: o.Commutator,
			OperatorNegator:    o.Negator,
			OperatorHashes:     o.Hashes,
			OperatorMerges:     o.Merges,
			OperatorCoreHash:   sourceBodyHash(OperatorCoreBodyHashInput(o.Body), o.Reconstructed),
		}}
	case *ir.OperatorClass:
		// Members/StorageType are populated unconditionally like OperatorFamily's
		// Members above, not gated on Reconstructed — needed for the differ's
		// structural comparison on both source-declared and introspected classes.
		//
		// FamilySchema/FamilyName resolve the same "omitted means implicit
		// same-name/same-schema-at-creation-time auto-family" fallback
		// diffOperatorClass's desired-side comparison already applies — the
		// snapshot must record the family's ACTUAL resolved identity (what
		// PostgreSQL really created), not the raw possibly-empty source
		// declaration, or a later diff comparing against this snapshot has
		// no way to know the family stayed behind in its original schema
		// when the class itself later moves via SET SCHEMA (real PostgreSQL
		// never moves an implicit auto-family along with its class).
		famSchema := o.FamilySchema
		if famSchema == "" {
			famSchema = o.Schema
		}
		famName := o.FamilyName
		if famName == "" {
			famName = o.Name
		}
		return &SnapObject{Kind: "operator_class", Opaque: &SnapOpaque{
			Kind: "operator_class", Schema: o.Schema, Name: o.Name, Using: o.AccessMethod, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			OperatorClassMembersStructured: true, OperatorClassMembers: toSnapOpFamilyMembers(o.Members),
			OperatorClassStorageType:  o.StorageType,
			OperatorClassFamilySchema: famSchema, OperatorClassFamilyName: famName,
		}}
	case *ir.OperatorFamily:
		// Members are populated unconditionally, never routed through
		// sourceBodyHash — they must persist for reconstructed/live objects
		// too (this is the G-live fix for this kind, built in from the
		// start rather than retrofitted the way the other 9
		// reconstruction-tier kinds needed).
		return &SnapObject{Kind: "operator_family", Opaque: &SnapOpaque{
			Kind: "operator_family", Schema: o.Schema, Name: o.Name, Using: o.AccessMethod, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			OpFamilyMembersStructured: true, OpFamilyMembers: toSnapOpFamilyMembers(o.Members),
		}}
	case *ir.Cast:
		return &SnapObject{Kind: "cast", Opaque: &SnapOpaque{
			Kind:         "cast",
			Name:         o.SourceType.String() + "->" + o.TargetType.String(),
			CastMethod:   o.Method,
			CastContext:  o.Context,
			CastFunction: o.Function,
			BodyHash:     sourceBodyHash(o.Body, o.Reconstructed),
			Comment:      o.Comment,
		}}
	case *ir.StatisticsObject:
		return &SnapObject{Kind: "statistics", Opaque: &SnapOpaque{
			Kind: "statistics", Schema: o.Schema, Name: o.Name,
			StatisticsStructured: true, StatisticsTable: o.Table, StatisticsKinds: o.Kinds, StatisticsColumns: o.Columns, StatisticsTarget: o.StatisticsTarget,
			BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
		}}
	case *ir.TSConfig:
		opaque := &SnapOpaque{
			Kind: "ts_config", Schema: o.Schema, Name: o.Name, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			TSConfigOwner: o.Owner,
		}
		for _, m := range o.Mappings {
			dicts := make([]string, len(m.Dictionaries))
			for i, d := range m.Dictionaries {
				dicts[i] = d.String()
			}
			opaque.Mappings = append(opaque.Mappings, SnapTSMapping{
				TokenTypes:   append([]string(nil), m.TokenTypes...),
				Dictionaries: dicts,
			})
		}
		return &SnapObject{Kind: "ts_config", Opaque: opaque}
	case *ir.TSDict:
		return &SnapObject{Kind: "ts_dict", Opaque: &SnapOpaque{
			Kind: "ts_dict", Schema: o.Schema, Name: o.Name, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
			TSDictOwner:             o.Owner,
			TSDictOptionsStructured: true, TSDictOptions: toSnapOptions(o.Options), TSDictTemplateName: o.TemplateName,
		}}
	case *ir.TSParser:
		return &SnapObject{Kind: "ts_parser", Opaque: &SnapOpaque{
			Kind: "ts_parser", Schema: o.Schema, Name: o.Name, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
		}}
	case *ir.TSTemplate:
		return &SnapObject{Kind: "ts_template", Opaque: &SnapOpaque{
			Kind: "ts_template", Schema: o.Schema, Name: o.Name, BodyHash: sourceBodyHash(o.Body, o.Reconstructed), Comment: o.Comment,
		}}
	case *ir.DefaultPrivileges:
		sdp := &SnapDefaultPrivileges{
			ForRole:    o.ForRole,
			InSchema:   o.InSchema,
			ObjectType: o.ObjectType,
		}
		for _, g := range o.Grants {
			sdp.Grants = append(sdp.Grants, toSnapGrant(g))
		}
		for _, r := range o.Revocations {
			sdp.Revocations = append(sdp.Revocations, toSnapRevocation(r))
		}
		return &SnapObject{Kind: "default_privileges", DefaultPrivileges: sdp}
	case *ir.ParameterPrivileges:
		spp := &SnapParameterPrivileges{}
		for _, g := range o.Grants {
			spp.Grants = append(spp.Grants, toSnapParamGrant(g))
		}
		for _, r := range o.Revocations {
			spp.Revocations = append(spp.Revocations, toSnapParamRevocation(r))
		}
		return &SnapObject{Kind: "parameter_privileges", ParameterPrivileges: spp}
	case *ir.VirtualType:
		return &SnapObject{Kind: "virtual_type", VirtualType: &SnapVirtualType{
			Schema:     o.Schema,
			Name:       o.Name,
			Body:       toSnapVtypeBody(o.Body),
			JsonFormat: o.JsonFormat,
			Comment:    o.Comment,
			NameMaps:   toSnapNameMaps(o.NameMaps),
		}}
	default:
		return nil
	}
}

// toSnapVtypeBody converts an ir.VtypeBody to its serialisable snapshot form.
func toSnapVtypeBody(body ir.VtypeBody) SnapVtypeBody {
	switch b := body.(type) {
	case ir.VtypeTypeRef:
		return SnapVtypeBody{Kind: "type_ref", Schema: b.Schema, Name: b.Name, IsArray: b.IsArray}
	case ir.VtypeComposite:
		fields := make([]SnapVtypeField, len(b.Fields))
		for i, f := range b.Fields {
			fields[i] = SnapVtypeField{
				Name: f.Name,
				Type: SnapVtypeBody{Kind: "type_ref", Schema: f.Type.Schema, Name: f.Type.Name, IsArray: f.Type.IsArray},
			}
		}
		return SnapVtypeBody{Kind: "composite", Fields: fields}
	case ir.VtypeUnion:
		members := make([]SnapVtypeBody, len(b.Members))
		for i, m := range b.Members {
			members[i] = toSnapVtypeBody(m)
		}
		return SnapVtypeBody{Kind: "union", Members: members}
	default:
		return SnapVtypeBody{Kind: "type_ref", Name: "unknown"}
	}
}

func toSnapSchema(o *ir.Schema) *SnapSchema {
	ss := &SnapSchema{
		Name:        o.Name,
		Owner:       o.Owner,
		Comment:     o.Comment,
		RenamedFrom: o.RenamedFrom,
		NameMaps:    toSnapNameMaps(o.NameMaps),
	}
	for _, g := range o.Grants {
		ss.Grants = append(ss.Grants, toSnapGrant(g))
	}
	for _, r := range o.Revocations {
		ss.Revocations = append(ss.Revocations, toSnapRevocation(r))
	}
	ss.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
	return ss
}

func toSnapExtension(o *ir.Extension) *SnapExtension {
	return &SnapExtension{
		Name:     o.Name,
		Schema:   o.Schema,
		Version:  o.Version,
		Comment:  o.Comment,
		NameMaps: toSnapNameMaps(o.NameMaps),
	}
}

// toSnapPartition converts an ir.Partition into a SnapPartition, recursing
// into nested sub-partitions (RFC Section 7.13). schema is the owning table's (or
// parent partition's) schema, since a partition itself carries no schema
// field.
func toSnapPartition(schema string, p *ir.Partition) SnapPartition {
	sp := SnapPartition{
		Schema: schema,
		Name:   p.Name,
		Bound:  p.Bounds,
	}
	if p.PartitionBy != nil {
		sp.PartitionBy = p.PartitionBy.Strategy + " (" + strings.Join(p.PartitionBy.Columns, ", ") + ")"
	}
	sp.Foreign = p.Foreign
	sp.ForeignServer = p.ForeignServer
	sp.ForeignOptions = flattenParams(p.ForeignOptions)
	for _, sub := range p.Partitions {
		sp.Partitions = append(sp.Partitions, toSnapPartition(schema, sub))
	}
	for _, cst := range p.Constraints {
		sp.Constraints = append(sp.Constraints, toSnapConstraint(cst))
	}
	return sp
}

func toSnapTable(o *ir.Table) *SnapTable {
	t := &SnapTable{
		Schema:               o.Schema,
		Name:                 o.Name,
		Unlogged:             o.Unlogged,
		Foreign:              o.Foreign,
		ForeignServer:        o.ForeignServer,
		ForeignOptions:       flattenParams(o.ForeignOptions),
		Owner:                o.Owner,
		Tablespace:           o.Tablespace,
		Comment:              o.Comment,
		RenamedFrom:          o.RenamedFrom,
		RenamedFromSchema:    o.RenamedFromSchema,
		Deprecated:           o.Deprecated,
		Protected:            o.Protected,
		DropCascade:          o.DropCascade,
		RLSEnabled:           o.RLSEnabled,
		RLSForced:            o.RLSForced,
		ReplicaIdentityMode:  o.ReplicaIdentity.Mode,
		ReplicaIdentityIndex: o.ReplicaIdentity.IndexName,
		ClusterOn:            o.ClusterOn,
		Inherits:             append([]string(nil), o.Inherits...),
		OfType:               o.OfType.String(),
		AccessMethod:         o.AccessMethod,
		StorageParams:        flattenParams(o.StorageParams),
	}
	if o.PartitionBy != nil {
		t.PartitionBy = o.PartitionBy.Strategy + " (" + strings.Join(o.PartitionBy.Columns, ", ") + ")"
	}
	for _, p := range o.Partitions {
		t.Partitions = append(t.Partitions, toSnapPartition(o.Schema, p))
	}
	for _, col := range o.Columns {
		t.Columns = append(t.Columns, toSnapColumn(col))
	}
	for _, cst := range o.Constraints {
		t.Constraints = append(t.Constraints, toSnapConstraint(cst))
	}
	for _, idx := range o.Indexes {
		t.Indexes = append(t.Indexes, ToSnapIndex(idx))
	}
	for _, pol := range o.Policies {
		t.Policies = append(t.Policies, toSnapPolicy(pol))
	}
	for _, trg := range o.Triggers {
		t.Triggers = append(t.Triggers, toSnapTrigger(trg))
	}
	for _, g := range o.Grants {
		t.Grants = append(t.Grants, toSnapGrant(g))
	}
	for _, r := range o.Revocations {
		t.Revocations = append(t.Revocations, toSnapRevocation(r))
	}
	t.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
	t.NameMaps = toSnapNameMaps(o.NameMaps)
	return t
}

func toSnapColumn(col *ir.Column) SnapColumn {
	sc := SnapColumn{
		Name:        col.Name,
		Type:        col.Type.String(),
		NotNull:     col.NotNull,
		Default:     col.Default,
		Comment:     col.Comment,
		Statistics:  col.Statistics,
		Compression: col.Compression,
		Storage:     col.Storage,
		Deprecated:  col.Deprecated,
		RenamedFrom: col.RenamedFrom,
		Serial:      col.Serial,
		Collation:   col.Collation,
	}
	if col.Identity != nil {
		var s string
		if col.Identity.Always {
			s = "ALWAYS"
		} else {
			s = "BY DEFAULT"
		}
		sc.Identity = &s
		sc.IdentityIncrementBy = col.Identity.IncrementBy
		sc.IdentityMinValue = col.Identity.MinValue
		sc.IdentityMaxValue = col.Identity.MaxValue
		sc.IdentityNoMinValue = col.Identity.NoMinValue
		sc.IdentityNoMaxValue = col.Identity.NoMaxValue
		sc.IdentityStartValue = col.Identity.StartValue
		sc.IdentityCache = col.Identity.Cache
		sc.IdentityCycle = col.Identity.Cycle
	}
	if col.Generated != nil {
		sc.Generated = &col.Generated.Expr
		sc.GeneratedVirtual = !col.Generated.Stored
	}
	for _, g := range col.Grants {
		sc.Grants = append(sc.Grants, toSnapGrant(g))
	}
	for _, r := range col.Revocations {
		sc.Revocations = append(sc.Revocations, toSnapRevocation(r))
	}
	sc.SecurityLabels = toSnapSecurityLabels(col.SecurityLabels)
	sc.NameMaps = toSnapNameMaps(col.NameMaps)
	return sc
}

func toSnapConstraint(cst *ir.Constraint) SnapConstraint {
	sc := SnapConstraint{
		Name:              cst.Name,
		Type:              cst.Type,
		Expr:              cst.Expr,
		NotValid:          cst.NotValid,
		Deferrable:        cst.Deferrable,
		NoInherit:         cst.NoInherit,
		NotEnforced:       cst.NotEnforced,
		InitiallyDeferred: cst.InitiallyDeferred,
	}
	if cst.Comment != nil {
		sc.Comment = *cst.Comment
	}
	return sc
}

// ToSnapIndex converts an ir.Index into its flat, fully comparable snapshot
// form. Exported so diff.diffIndexes can build the same representation from
// the desired side and compare it against the stored snapshot with `==` to
// detect a same-named index whose definition changed.
// flattenParams renders an ordered key/value param list (Index.With,
// Table.ForeignOptions) as a comma-separated "key=value" string for
// snapshot storage/comparison. Reloption/option values are often quoted by
// PostgreSQL's own reconstruction (pg_get_indexdef, pg_options_to_table) but
// not necessarily by hand-written source — stripping one layer of
// surrounding single quotes keeps the two spellings comparing equal, so a
// live/introspected catalog never shows spurious drift against unchanged
// source.
func flattenParams(params []pipeline.StorageParam) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		v := p.Value
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		parts = append(parts, p.Key+"="+v)
	}
	return strings.Join(parts, ", ")
}

// flattenTypeRefs renders each TypeRef's canonical string form (RFC audit
// item #26's TRANSFORM FOR TYPE list diffing input) — order-preserving,
// since PostgreSQL's own comma-separated FOR TYPE clause is itself ordered.
func flattenTypeRefs(refs []ir.TypeRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return out
}

// toSnapFuncConfigs converts ir.FuncConfig entries (Section 9.2's SET
// clause list) to their serialised form.
func toSnapFuncConfigs(configs []ir.FuncConfig) []SnapFuncConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]SnapFuncConfig, len(configs))
	for i, c := range configs {
		out[i] = SnapFuncConfig{Param: c.Param, Value: c.Value, FromCurrent: c.FromCurrent}
	}
	return out
}

func ToSnapIndex(idx *ir.Index) SnapIndex {
	cols := make([]string, 0, len(idx.Columns))
	for _, c := range idx.Columns {
		var s string
		if c.Name != "" {
			s = c.Name
		} else if c.Expr != nil {
			s = "(" + c.Expr.Text + ")"
		}
		// Collation/OpClass(Params) must be part of this comparison string
		// too, or a changed one is invisible to diffing — matching real
		// PostgreSQL's own index_elem clause order (COLLATE/opclass come
		// before ASC/DESC/NULLS, confirmed live).
		if c.Collation != nil {
			s += " COLLATE " + c.Collation.Name
		}
		if c.OpClass != nil {
			s += " " + c.OpClass.Name
			if len(c.OpClassParams) > 0 {
				s += "(" + flattenParams(c.OpClassParams) + ")"
			}
		}
		if c.SortOrder != "" {
			s += " " + c.SortOrder
		}
		if c.Nulls != "" {
			s += " NULLS " + c.Nulls
		}
		cols = append(cols, s)
	}
	si := SnapIndex{
		Name:             idx.Name,
		Unique:           idx.Unique,
		Method:           idx.Method,
		Columns:          strings.Join(cols, ", "),
		Include:          strings.Join(idx.Include, ", "),
		NullsNotDistinct: idx.NullsNotDistinct,
		With:             flattenParams(idx.With),
	}
	if idx.Where != nil {
		si.Where = *idx.Where
	}
	if idx.Tablespace != nil {
		si.Tablespace = *idx.Tablespace
	}
	if idx.Comment != nil {
		si.Comment = *idx.Comment
	}
	return si
}

func toSnapPolicy(pol *ir.Policy) SnapPolicy {
	sp := SnapPolicy{
		Name:       pol.Name,
		Command:    pol.Command,
		Permissive: pol.Permissive,
		Roles:      append([]string(nil), pol.Roles...),
	}
	if pol.Using != nil {
		sp.Using = *pol.Using
	}
	if pol.WithCheck != nil {
		sp.WithCheck = *pol.WithCheck
	}
	if pol.Comment != nil {
		sp.Comment = *pol.Comment
	}
	return sp
}

func toSnapTrigger(trg *ir.Trigger) SnapTrigger {
	st := SnapTrigger{
		Name:                trg.Name,
		When:                trg.When,
		Events:              strings.Join(trg.Events, ", "),
		ForEach:             trg.ForEach,
		UpdateOfColumns:     strings.Join(trg.UpdateOfColumns, ", "),
		Function:            trg.Function,
		EnableState:         trg.EnableState,
		DependsOnExtensions: append([]string(nil), trg.DependsOnExtensions...),
	}
	if trg.OldTransitionName != nil {
		st.OldTransitionName = *trg.OldTransitionName
	}
	if trg.NewTransitionName != nil {
		st.NewTransitionName = *trg.NewTransitionName
	}
	if trg.Condition != nil {
		st.Condition = *trg.Condition
	}
	if trg.Comment != nil {
		st.Comment = *trg.Comment
	}
	return st
}

func toSnapGrant(g ir.Grant) SnapGrant {
	return SnapGrant{
		Privileges: g.Privileges,
		Roles:      g.Roles,
		WithGrant:  g.WithGrant,
		GrantedBy:  g.GrantedBy,
	}
}

// toSnapRevocation converts an explicit ir.Revocation into a SnapGrant for
// storage/comparison purposes. Reuses SnapGrant (rather than a dedicated type)
// to match the existing DefaultPrivileges precedent; Cascade is intentionally
// not stored, same as that precedent — a revocation is matched for diffing
// purposes by privileges+roles only.
func toSnapRevocation(r ir.Revocation) SnapGrant {
	return SnapGrant{
		Privileges: r.Privileges,
		Roles:      r.Roles,
		GrantedBy:  r.GrantedBy,
	}
}

func toSnapParamGrant(g ir.ParameterGrant) SnapParamGrant {
	return SnapParamGrant{
		Privileges: g.Privileges,
		Parameters: g.Parameters,
		Roles:      g.Roles,
		WithGrant:  g.WithGrant,
		GrantedBy:  g.GrantedBy,
	}
}

// toSnapParamRevocation mirrors toSnapRevocation — Cascade is intentionally
// not stored, same as that precedent.
func toSnapParamRevocation(r ir.ParameterRevocation) SnapParamGrant {
	return SnapParamGrant{
		Privileges: r.Privileges,
		Parameters: r.Parameters,
		Roles:      r.Roles,
		GrantedBy:  r.GrantedBy,
	}
}

func toSnapView(o *ir.View) *SnapView {
	sv := &SnapView{
		Schema:        o.Schema,
		Name:          o.Name,
		Query:         o.Query,
		Owner:         o.Owner,
		Comment:       o.Comment,
		Deprecated:    o.Deprecated,
		Recursive:     o.Recursive,
		WithNoData:    o.WithNoData,
		Tablespace:    o.Tablespace,
		StorageParams: flattenParams(o.StorageParams),
	}
	for _, g := range o.Grants {
		sv.Grants = append(sv.Grants, toSnapGrant(g))
	}
	for _, r := range o.Revocations {
		sv.Revocations = append(sv.Revocations, toSnapRevocation(r))
	}
	for _, idx := range o.Indexes {
		sv.Indexes = append(sv.Indexes, ToSnapIndex(idx))
	}
	sv.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
	sv.NameMaps = toSnapNameMaps(o.NameMaps)
	return sv
}

func toSnapFunction(o *ir.Function) *SnapFunction {
	sf := &SnapFunction{
		Schema:      o.Schema,
		Name:        o.Name,
		Args:        ir.ArgsKey(o.Args),
		ReturnType:  o.ReturnType.String(),
		ReturnsSet:  o.ReturnType.SetOf,
		ReturnTable: ir.FormatTableColumns(ir.FuncTableColumns(o.Args)),
		Language:    o.Attrs.Language,
		Volatility:  o.Attrs.Volatility,
		Parallel:    o.Attrs.Parallel,
		Cost:        o.Attrs.Cost,
		Rows:        o.Attrs.Rows,
		BodyHash:    o.BodyHash,
		Comment:     o.Comment,
		Deprecated:  o.Deprecated,
		Owner:       o.Owner,
		Leakproof:   o.Attrs.Leakproof,
		Transforms:  flattenTypeRefs(o.Attrs.Transforms),
		ObjFile:     o.Attrs.ObjFile,
		LinkSymbol:  o.Attrs.LinkSymbol,
		AtomicBody:  o.Attrs.AtomicBody,
		Strict:      o.Attrs.Strict,
		SecurityDef: o.Attrs.SecurityDef,
		Config:      toSnapFuncConfigs(o.Attrs.Config),
	}
	for _, g := range o.Grants {
		sf.Grants = append(sf.Grants, toSnapGrant(g))
	}
	for _, r := range o.Revocations {
		sf.Revocations = append(sf.Revocations, toSnapRevocation(r))
	}
	sf.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
	sf.NameMaps = toSnapNameMaps(o.NameMaps)
	sf.DependsOnExtensions = append([]string(nil), o.DependsOnExtensions...)
	return sf
}

func toSnapType(o *ir.Type) *SnapType {
	st := &SnapType{
		Schema:  o.Schema,
		Name:    o.Name,
		Variant: o.Variant,
		Values:  o.EnumValues,
		Comment: o.Comment,
		Owner:   o.Owner,
	}
	if o.Variant == "RANGE" {
		st.BodyHash = sourceBodyHash(RangeBodyHashInput(o.Body), o.Reconstructed)
		st.MultirangeName = o.MultirangeName
	}
	if o.Variant == "BASE" {
		st.BodyHash = sourceBodyHash(o.Body, o.Reconstructed)
	}
	if o.Variant == "BASE" {
		st.BaseStructured = true
		st.BaseImmutableHash = sourceBodyHash(BaseBodyHashInput(o.Body), o.Reconstructed)
		st.BaseReceive = o.BaseReceive
		st.BaseSend = o.BaseSend
		st.BaseTypmodIn = o.BaseTypmodIn
		st.BaseTypmodOut = o.BaseTypmodOut
		st.BaseAnalyze = o.BaseAnalyze
		st.BaseSubscript = o.BaseSubscript
		st.BaseStorage = o.BaseStorage
	}
	if o.Variant == "DOMAIN" {
		st.DomainBaseType = o.DomainBaseType.String()
		st.DomainCollation = o.DomainCollation
		st.DomainDefault = o.DomainDefault
		st.DomainNotNull = o.DomainNotNull
		for _, cst := range o.DomainConstraints {
			st.DomainConstraints = append(st.DomainConstraints, toSnapConstraint(cst))
		}
	}
	for _, attr := range o.CompositeAttrs {
		st.CompositeAttrs = append(st.CompositeAttrs, toSnapColumn(attr))
	}
	for _, g := range o.Grants {
		st.Grants = append(st.Grants, toSnapGrant(g))
	}
	for _, r := range o.Revocations {
		st.Revocations = append(st.Revocations, toSnapRevocation(r))
	}
	st.SecurityLabels = toSnapSecurityLabels(o.SecurityLabels)
	st.NameMaps = toSnapNameMaps(o.NameMaps)
	return st
}

func toSnapSequence(o *ir.Sequence) *SnapSequence {
	ss := &SnapSequence{
		Schema:         o.Schema,
		Name:           o.Name,
		Unlogged:       o.Unlogged,
		Owner:          o.Owner,
		Comment:        o.Comment,
		IncrementBy:    o.IncrementBy,
		MinValue:       o.MinValue,
		MaxValue:       o.MaxValue,
		NoMinValue:     o.NoMinValue,
		NoMaxValue:     o.NoMaxValue,
		StartValue:     o.StartValue,
		Cache:          o.Cache,
		Cycle:          o.Cycle != nil && *o.Cycle,
		OwnedBy:        o.OwnedBy,
		SecurityLabels: toSnapSecurityLabels(o.SecurityLabels),
		NameMaps:       toSnapNameMaps(o.NameMaps),
	}
	if o.AsType != nil {
		ss.AsType = o.AsType.String()
	}
	for _, g := range o.Grants {
		ss.Grants = append(ss.Grants, toSnapGrant(g))
	}
	for _, r := range o.Revocations {
		ss.Revocations = append(ss.Revocations, toSnapRevocation(r))
	}
	return ss
}

func toSnapRole(o *ir.Role) *SnapRole {
	var pwHash string
	if o.Password != nil {
		pwHash = hashBodyStr(*o.Password)
	}
	var configs []SnapRoleConfig
	for _, c := range o.Configs {
		configs = append(configs, SnapRoleConfig{
			Param: c.Param, Value: c.Value, FromCurrent: c.FromCurrent,
			Reset: c.Reset, ResetAll: c.ResetAll, InDatabase: c.InDatabase,
		})
	}
	var memberships []SnapRoleMembership
	for _, m := range o.Memberships {
		memberships = append(memberships, SnapRoleMembership{
			Role: m.Role, Direction: m.Direction, Admin: m.Admin, Inherit: m.Inherit, Set: m.Set,
		})
	}
	return &SnapRole{
		Configs:         configs,
		Memberships:     memberships,
		Name:            o.Name,
		CanLogin:        o.CanLogin,
		Superuser:       o.Superuser,
		CreateDB:        o.CreateDB,
		CreateRole:      o.CreateRole,
		Inherit:         o.Inherit,
		IsReplication:   o.IsReplication,
		BypassRLS:       o.BypassRLS,
		ConnectionLimit: o.ConnectionLimit,
		PasswordHash:    pwHash,
		ValidUntil:      o.ValidUntil,
		Comment:         o.Comment,
		SecurityLabels:  toSnapSecurityLabels(o.SecurityLabels),
		NameMaps:        toSnapNameMaps(o.NameMaps),
	}
}
