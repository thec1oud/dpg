package snapshot

// SnapNameMapEntry is one NAME MAP entry stored in the snapshot for an object.
// Exactly one of Rule or Name is non-empty:
//   - Rule is a DPG naming-convention rule keyword (e.g. "LOWER_SNAKE_CASE").
//   - Name is a literal target identifier (from a double-quoted source value).
type SnapNameMapEntry struct {
	Tool string `json:"tool"`
	Rule string `json:"rule,omitempty"`
	Name string `json:"name,omitempty"`
}

// SnapObject is the discriminated JSON form stored per object in the snapshot.
// The Kind field allows the Differ to load the right Go type.
type SnapObject struct {
	Kind string `json:"kind"`
	// One of the following is populated, depending on Kind.
	Table               *SnapTable               `json:"table,omitempty"`
	View                *SnapView                `json:"view,omitempty"`
	Function            *SnapFunction            `json:"function,omitempty"`
	Type                *SnapType                `json:"type,omitempty"`
	Schema              *SnapSchema              `json:"schema,omitempty"`
	Extension           *SnapExtension           `json:"extension,omitempty"`
	Sequence            *SnapSequence            `json:"sequence,omitempty"`
	Role                *SnapRole                `json:"role,omitempty"`
	VirtualType         *SnapVirtualType         `json:"virtual_type,omitempty"`
	DefaultPrivileges   *SnapDefaultPrivileges   `json:"default_privileges,omitempty"`
	ParameterPrivileges *SnapParameterPrivileges `json:"parameter_privileges,omitempty"`
	Opaque              *SnapOpaque              `json:"opaque,omitempty"`
}

// SnapOpaque covers body-based objects: Procedure, Aggregate, Tablespace, FDW,
// ForeignServer, UserMapping, Publication, Subscription, EventTrigger, Collation,
// Operator, OperatorClass, OperatorFamily, Cast, StatisticsObject, and TS objects.
type SnapOpaque struct {
	Kind   string `json:"kind"` // e.g. "procedure", "tablespace"
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name"`
	// Args is reused across two unrelated purposes, distinguished by Kind:
	// for procedure/aggregate it's the type-only arg list used for identity
	// AND for DROP PROCEDURE/AGGREGATE's "(args)" clause; for operator it's
	// "lefttype, righttype" (see ir.OperandsKey) used for DROP OPERATOR's
	// mandatory operand-type clause, in the same string-goes-straight-into-
	// the-parens shape.
	Args     string  `json:"args,omitempty"`
	Using    string  `json:"using,omitempty"` // index access method (operator_class/operator_family)
	BodyHash string  `json:"body_hash,omitempty"`
	Comment  *string `json:"comment,omitempty"`
	// Deprecated mirrors SnapFunction.Deprecated (RFC audit items #8/#9) —
	// procedure and aggregate only.
	Deprecated  *string     `json:"deprecated,omitempty"`
	Grants      []SnapGrant `json:"grants,omitempty"`      // aggregate and procedure
	Revocations []SnapGrant `json:"revocations,omitempty"` // procedure and aggregate
	// SecurityLabels (RFC Section 14.11) covers every opaque kind SECURITY LABEL
	// applies to: procedure, aggregate, tablespace, publication,
	// subscription, event_trigger.
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	Mappings       []SnapTSMapping     `json:"mappings,omitempty"` // ts_config only (RFC Section 12.1 MAPPING FOR)
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
	// TablespaceLocation (RFC Section 14.7), CastMethod/CastContext/CastFunction
	// (RFC Section 14.5), and EventTriggerEvent/EventTriggerTags/
	// EventTriggerFunction (RFC Section 14.1) are structured diffing inputs for
	// their respective kinds only — see ir.Tablespace.Location/ir.Cast.
	// Method/ir.EventTrigger.Event's doc comments for why BodyHash alone
	// was insufficient (it goes unset, via Reconstructed, on every live
	// path, so a live-catalog-only change was silently invisible).
	TablespaceLocation string `json:"tablespace_location,omitempty"`
	// TablespaceOwner is RFC audit item #80's inline `OWNER` diffing input
	// — see ir.Tablespace.Owner's doc comment.
	TablespaceOwner *string `json:"tablespace_owner,omitempty"`
	// TablespaceStorageParams is Section 14.7's WITH (...) diffing input,
	// flattened via flattenParams the same way Table.StorageParams is.
	TablespaceStorageParams string   `json:"tablespace_storage_params,omitempty"`
	CastMethod              string   `json:"cast_method,omitempty"`
	CastContext             string   `json:"cast_context,omitempty"`
	CastFunction            string   `json:"cast_function,omitempty"`
	EventTriggerEvent       string   `json:"event_trigger_event,omitempty"`
	EventTriggerTags        []string `json:"event_trigger_tags,omitempty"`
	EventTriggerFunction    string   `json:"event_trigger_function,omitempty"`
	// EventTriggerOwner is Section 14.1's ALTER EVENT TRIGGER ... OWNER TO
	// diffing input — same shape as PublicationOwner above.
	EventTriggerOwner *string `json:"event_trigger_owner,omitempty"`
	// EventTriggerEnableState is Section 14.1's DISABLED/ENABLE REPLICA/
	// ENABLE ALWAYS diffing input — see ir.EventTrigger.EnableState's doc
	// comment.
	EventTriggerEnableState string `json:"event_trigger_enable_state,omitempty"`
	// TSDictOwner is Section 12.2's ALTER TEXT SEARCH DICTIONARY ... OWNER
	// TO diffing input — same shape as PublicationOwner/EventTriggerOwner
	// above. TSParser/TSTemplate have no OWNER concept in real PostgreSQL
	// (confirmed via \h ALTER TEXT SEARCH PARSER/TEMPLATE), so neither gets
	// an equivalent field.
	TSDictOwner *string `json:"ts_dict_owner,omitempty"`
	// TSConfigOwner is Section 12.1's ALTER TEXT SEARCH CONFIGURATION ...
	// OWNER TO diffing input — same shape as TSDictOwner above.
	TSConfigOwner *string `json:"ts_config_owner,omitempty"`
	// TSDictOptionsStructured/TSDictOptions/TSDictTemplateName are Section
	// 12.2's option-change diffing inputs (audit item #52) — see
	// ir.TSDict.Options' doc comment. Structured sentinel follows the same
	// "not every real dictionary declares options, so an empty list is a
	// legitimate live state, not a stale-snapshot signal" reasoning as
	// OptionsStructured/AggregateOptionsStructured above — per-kind-named
	// rather than reusing the shared OptionsStructured field, matching
	// AggregateOptionsStructured's own precedent. TSDictTemplateName is
	// deliberately name-only, no schema counterpart: confirmed live that an
	// unqualified TEMPLATE reference (the overwhelmingly common case — a
	// built-in template) resolves via search_path to pg_catalog, not the
	// dictionary's own schema, so falling back to the dictionary's schema
	// (the convention every other RENAMED-FROM/family-style field in this
	// file uses) would misdetect the ordinary case as changed on every
	// plan — the same false-positive class this file's other Structured
	// fallbacks exist specifically to avoid. A same-named template in two
	// different schemas is accepted as an unrepresented edge case.
	TSDictOptionsStructured bool           `json:"ts_dict_options_structured,omitempty"`
	TSDictOptions           []SnapOptionKV `json:"ts_dict_options,omitempty"`
	TSDictTemplateName      string         `json:"ts_dict_template_name,omitempty"`
	// ProcedureDependsOnExtensions is Section 9.1's `[NO] DEPENDS ON
	// EXTENSION` diffing input for Procedure specifically (Function uses
	// its own dedicated SnapFunction.DependsOnExtensions field instead,
	// since Procedure — unlike Function — routes through the generic
	// SnapOpaque shape).
	ProcedureDependsOnExtensions []string `json:"procedure_depends_on_extensions,omitempty"`
	// ProcedureOwner/AggregateOwner are RFC audit item #70's
	// ALTER PROCEDURE/AGGREGATE ... OWNER TO diffing inputs — see
	// ir.Procedure.Owner/ir.Aggregate.Owner's doc comments. Function uses
	// its own dedicated SnapFunction.Owner field instead, same reasoning as
	// ProcedureDependsOnExtensions above.
	ProcedureOwner *string `json:"procedure_owner,omitempty"`
	AggregateOwner *string `json:"aggregate_owner,omitempty"`
	// ProcedureTransforms/ProcedureObjFile/ProcedureLinkSymbol/
	// ProcedureAtomicBody mirror SnapFunction's identical RFC audit items
	// #26/#27/#28 diffing inputs — see ir.FuncAttrs' doc comments. Procedure
	// uses these dedicated fields instead of SnapFunction's (same reasoning
	// as ProcedureDependsOnExtensions above); Leakproof has no Procedure
	// counterpart at all (real PostgreSQL rejects it there, confirmed live:
	// "invalid attribute in procedure definition").
	ProcedureTransforms []string `json:"procedure_transforms,omitempty"`
	ProcedureObjFile    *string  `json:"procedure_obj_file,omitempty"`
	ProcedureLinkSymbol *string  `json:"procedure_link_symbol,omitempty"`
	ProcedureAtomicBody bool     `json:"procedure_atomic_body,omitempty"`
	// ProcedureSecurityDef/ProcedureConfig are Section 9.3's SECURITY
	// DEFINER / Section 9.2's SET diffing inputs for Procedure — both were
	// previously missing entirely (not just here but in
	// RenderCreateProcedureSQL's emission), so a procedure declaring either
	// silently lost it on the very first apply (createProcedure recreates
	// via the same renderer used for a real recreate). Procedure uses
	// these dedicated fields instead of SnapFunction's, same reasoning as
	// ProcedureDependsOnExtensions above.
	ProcedureSecurityDef bool         `json:"procedure_security_def,omitempty"`
	ProcedureConfig      []SnapFuncConfig `json:"procedure_config,omitempty"`
	// OptionsStructured/FDWHandler/FDWValidator/FDWOptions/ServerFDWName/
	// ServerType/ServerVersion/ServerOptions/UserMappingOptions are RFC
	// Section 14.8/Section 14.9/Section 14.10's structured diffing inputs for fdw/server/
	// user_mapping respectively. Unlike Tablespace/Cast/EventTrigger
	// (Tier 1), none of these three kinds has a field guaranteed non-empty
	// on every real object (a bare FOREIGN DATA WRAPPER with no HANDLER/
	// VALIDATOR/OPTIONS is valid), so the usual "Go zero value means
	// stale snapshot" guard doesn't work here — OptionsStructured is an
	// explicit sentinel instead, set true only by current code, so its
	// absence unambiguously means "snapshot predates this feature."
	OptionsStructured bool           `json:"options_structured,omitempty"`
	FDWHandler        string         `json:"fdw_handler,omitempty"`
	FDWValidator      string         `json:"fdw_validator,omitempty"`
	FDWOptions        []SnapOptionKV `json:"fdw_options,omitempty"`
	// FDWOwner is Section 14.8's ALTER FOREIGN DATA WRAPPER ... OWNER TO
	// diffing input — same shape as ServerOwner below.
	FDWOwner      *string `json:"fdw_owner,omitempty"`
	ServerFDWName string  `json:"server_fdw_name,omitempty"`
	ServerType        *string        `json:"server_type,omitempty"`
	ServerVersion     *string        `json:"server_version,omitempty"`
	ServerOptions     []SnapOptionKV `json:"server_options,omitempty"`
	// ServerOwner is RFC audit item #79's ALTER SERVER ... OWNER TO diffing
	// input — same shape as PublicationOwner/EventTriggerOwner/TSDictOwner
	// above.
	ServerOwner        *string        `json:"server_owner,omitempty"`
	UserMappingOptions []SnapOptionKV `json:"user_mapping_options,omitempty"`
	// PublicationStructured/PublicationAllTables/PublicationTables/
	// PublicationInsert/Update/Delete/Truncate are RFC Section 13.1's structured
	// diffing inputs — see ir.Publication.AllTables' doc comment.
	// PublicationTables holds "schema.name" qualified strings, compared as
	// a set (order-independent). PublicationStructured is the same
	// explicit-sentinel pattern as OptionsStructured above (Publish flags
	// default true, AllTables can legitimately be false — no field is a
	// reliable "not yet populated" signal on its own).
	// PublicationOwner is RFC audit item #7's ALTER PUBLICATION ... OWNER TO
	// diffing input — see ir.Publication.Owner's doc comment.
	PublicationOwner             *string  `json:"publication_owner,omitempty"`
	PublicationStructured        bool     `json:"publication_structured,omitempty"`
	PublicationAllTables         bool     `json:"publication_all_tables,omitempty"`
	PublicationTables            []string `json:"publication_tables,omitempty"`
	PublicationInsert            bool     `json:"publication_insert,omitempty"`
	PublicationUpdate            bool     `json:"publication_update,omitempty"`
	PublicationDelete            bool     `json:"publication_delete,omitempty"`
	PublicationTruncate          bool     `json:"publication_truncate,omitempty"`
	PublicationHasFilteredTables bool     `json:"publication_has_filtered_tables,omitempty"`
	// CollationStructured/CollationProvider/CollationCollate/
	// CollationCtype/CollationICULocale/CollationDeterministic are RFC
	// Section 14.2's structured diffing inputs — see ir.Collation.Provider's doc
	// comment. CollationStructured is the same explicit-sentinel pattern
	// as OptionsStructured above (Provider "c" is itself PostgreSQL's real
	// default, not a reliable "unpopulated" signal).
	CollationStructured    bool    `json:"collation_structured,omitempty"`
	CollationProvider      string  `json:"collation_provider,omitempty"`
	CollationCollate       *string `json:"collation_collate,omitempty"`
	CollationCtype         *string `json:"collation_ctype,omitempty"`
	CollationICULocale     *string `json:"collation_icu_locale,omitempty"`
	CollationDeterministic bool    `json:"collation_deterministic,omitempty"`
	// CollationOwner is RFC audit item #81's ALTER COLLATION ... OWNER TO
	// diffing input — see ir.Collation.Owner's doc comment.
	CollationOwner *string `json:"collation_owner,omitempty"`
	// CollationRules is RFC audit item #111's PG16+ ICU RULES diffing
	// input — see ir.Collation.Rules' doc comment.
	CollationRules *string `json:"collation_rules,omitempty"`
	// SubscriptionOwner is Section 13.2's ALTER SUBSCRIPTION ... OWNER TO
	// diffing input — same shape as CollationOwner above.
	SubscriptionOwner *string `json:"subscription_owner,omitempty"`
	// OperatorStructured/OperatorRestrict/OperatorJoin/OperatorCommutator/
	// OperatorNegator/OperatorHashes/OperatorMerges are RFC Section 14.3's
	// optimizer-hint structured diffing inputs — see ir.Operator.Restrict's
	// doc comment. OperatorStructured is the same explicit-sentinel pattern
	// as CollationStructured above: Hashes/Merges being false is itself a
	// legitimate declared state (most operators have neither), not a
	// reliable "predates this field" signal.
	OperatorStructured bool    `json:"operator_structured,omitempty"`
	OperatorRestrict   *string `json:"operator_restrict,omitempty"`
	OperatorJoin       *string `json:"operator_join,omitempty"`
	OperatorCommutator *string `json:"operator_commutator,omitempty"`
	OperatorNegator    *string `json:"operator_negator,omitempty"`
	OperatorHashes     bool    `json:"operator_hashes,omitempty"`
	OperatorMerges     bool    `json:"operator_merges,omitempty"`
	// OperatorCoreHash is BodyHash's counterpart with the 6 hint properties'
	// text stripped first (see OperatorCoreBodyHashInput) — a hint-only
	// change must not trip the core-definition DROP+CREATE comparison, the
	// same reasoning as BaseImmutableHash for BASE types.
	OperatorCoreHash string `json:"operator_core_hash,omitempty"`
	// StatisticsStructured/StatisticsTable/StatisticsKinds/
	// StatisticsColumns/StatisticsTarget are RFC Section 14.6's structured
	// diffing inputs — see ir.StatisticsObject.Table's doc comment.
	// StatisticsStructured is the same explicit-sentinel pattern as
	// OptionsStructured above (an empty Kinds/Columns list is not itself
	// a reliable "unpopulated" signal, since a stale snapshot and a
	// freshly-populated-but-genuinely-empty one would look identical).
	StatisticsStructured bool     `json:"statistics_structured,omitempty"`
	StatisticsTable      string   `json:"statistics_table,omitempty"`
	StatisticsKinds      []string `json:"statistics_kinds,omitempty"`
	StatisticsColumns    []string `json:"statistics_columns,omitempty"`
	StatisticsTarget     *int     `json:"statistics_target,omitempty"`
	// OpFamilyMembersStructured/OpFamilyMembers are RFC Section 14.4's structured
	// diffing input for an operator family's "loose" members — see
	// ir.OperatorFamily.Members's doc comment. OpFamilyMembersStructured is
	// the same explicit-sentinel pattern as OptionsStructured above: a real
	// family can legitimately have zero loose members, so an empty
	// OpFamilyMembers slice can't distinguish "none declared" from
	// "snapshot predates this feature" — getting that wrong would emit an
	// ALTER OPERATOR FAMILY ... ADD for a member that already exists, which
	// genuinely errors in PostgreSQL (there is no ADD ... IF NOT EXISTS).
	// Populated unconditionally, never gated on Reconstructed — see
	// convert.go's OperatorFamily case for why.
	OpFamilyMembersStructured bool                 `json:"op_family_members_structured,omitempty"`
	OpFamilyMembers           []SnapOpFamilyMember `json:"op_family_members,omitempty"`
	// OperatorClassMembersStructured/OperatorClassMembers/OperatorClassStorageType
	// mirror OpFamilyMembersStructured/OpFamilyMembers above, for an operator
	// class's own AS-list (see ir.OperatorClass.Members's doc comment). Used
	// only for a structural equality check in the differ — PostgreSQL has no
	// incremental ALTER OPERATOR CLASS, so any genuine member-list change
	// still resolves to DROP+CREATE; this exists solely to avoid a false
	// DESTRUCTIVE diff when BodyHash differs for purely cosmetic reasons
	// (whitespace, operator qualification, type spelling) between
	// hand-written source and an introspected reconstruction. The sentinel
	// distinguishes "snapshot predates this feature" (no structured
	// comparison possible, fall back to raw BodyHash) from a class that
	// genuinely has this data captured.
	OperatorClassMembersStructured bool                 `json:"operator_class_members_structured,omitempty"`
	OperatorClassMembers           []SnapOpFamilyMember `json:"operator_class_members,omitempty"`
	OperatorClassStorageType       string               `json:"operator_class_storage_type,omitempty"`
	// OperatorClassFamilySchema/Name mirror ir.OperatorClass.FamilySchema/
	// FamilyName — captured so the differ's structural comparison can detect
	// a FAMILY-clause-only change (which, like any AS-list change, has no
	// incremental ALTER and must still resolve to DROP+CREATE) even though
	// it wouldn't otherwise show up in Members/StorageType.
	OperatorClassFamilySchema string `json:"operator_class_family_schema,omitempty"`
	OperatorClassFamilyName   string `json:"operator_class_family_name,omitempty"`
	// AggregateOptionsStructured/AggregateOptions mirror
	// OperatorClassMembersStructured/OperatorClassMembers above, for a CREATE
	// AGGREGATE's SFUNC/STYPE/INITCOND/... option list (see
	// ir.Aggregate.Options's doc comment). Used only for a structural
	// equality check in the differ — PostgreSQL has no incremental ALTER
	// AGGREGATE, so any genuine option change still resolves to DROP+CREATE;
	// this exists solely to avoid a false DESTRUCTIVE diff when BodyHash
	// differs for purely cosmetic reasons (keyword case, option order)
	// between hand-written source and an introspected reconstruction. The
	// sentinel distinguishes "snapshot predates this feature" (no structured
	// comparison possible, fall back to raw BodyHash) from an aggregate that
	// genuinely has this data captured.
	AggregateOptionsStructured bool           `json:"aggregate_options_structured,omitempty"`
	AggregateOptions           []SnapOptionKV `json:"aggregate_options,omitempty"`
}

// SnapOpFamilyMember is one "loose" OPERATOR FAMILY member (RFC Section 14.4) —
// the snapshot-side mirror of pipeline.OpFamilyMember.
type SnapOpFamilyMember struct {
	IsFunction       bool     `json:"is_function,omitempty"`
	Number           int      `json:"number"`
	NameSchema       string   `json:"name_schema,omitempty"`
	Name             string   `json:"name"`
	LeftType         string   `json:"left_type"`
	RightType        string   `json:"right_type"`
	FuncArgs         []string `json:"func_args,omitempty"`
	OrderBy          bool     `json:"order_by,omitempty"`
	SortFamilySchema string   `json:"sort_family_schema,omitempty"`
	SortFamilyName   string   `json:"sort_family_name,omitempty"`
}

// SnapOptionKV is one OPTIONS (...) key/value entry, used by the
// fdw/server/user_mapping kinds above.
type SnapOptionKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SnapTSMapping is one MAPPING FOR entry (RFC Section 12.1) on a text search
// configuration. Dictionaries is a fallback chain — real PG syntax, tried in
// order until one recognizes the token.
type SnapTSMapping struct {
	TokenTypes   []string `json:"token_types"`
	Dictionaries []string `json:"dictionaries"`
}

type SnapSchema struct {
	Name           string              `json:"name"`
	Owner          *string             `json:"owner,omitempty"`
	Comment        *string             `json:"comment,omitempty"`
	RenamedFrom    *string             `json:"renamed_from,omitempty"`
	Grants         []SnapGrant         `json:"grants,omitempty"`
	Revocations    []SnapGrant         `json:"revocations,omitempty"`
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

type SnapExtension struct {
	Name     string             `json:"name"`
	Schema   *string            `json:"schema,omitempty"`
	Version  *string            `json:"version,omitempty"`
	Comment  *string            `json:"comment,omitempty"`
	NameMaps []SnapNameMapEntry `json:"name_maps,omitempty"`
}

type SnapTable struct {
	Schema         string  `json:"schema"`
	Name           string  `json:"name"`
	Unlogged       bool    `json:"unlogged,omitempty"`
	Foreign        bool    `json:"foreign,omitempty"`
	ForeignServer  *string `json:"foreign_server,omitempty"`
	ForeignOptions string  `json:"foreign_options,omitempty"` // comma-separated key=value options
	Owner          *string `json:"owner,omitempty"`
	Tablespace     *string `json:"tablespace,omitempty"`
	Comment        *string `json:"comment,omitempty"`
	// OfType/AccessMethod mirror ir.Table's identically-named fields
	// (Section 7.1); "" means unset (ordinary table / cluster default).
	OfType       string `json:"of_type,omitempty"`
	AccessMethod string `json:"access_method,omitempty"`
	// StorageParams is a WITH (...) clause's key=value pairs, flattened via
	// flattenParams the same way ForeignOptions is — see its doc comment.
	StorageParams string  `json:"storage_params,omitempty"`
	RenamedFrom   *string `json:"renamed_from,omitempty"`
	// RenamedFromSchema is the schema the RENAMED FROM name lived in, when the
	// directive was schema-qualified (a rename combined with a SET SCHEMA
	// move) — see ir.Table.RenamedFromSchema's identical doc comment.
	RenamedFromSchema *string `json:"renamed_from_schema,omitempty"`
	Deprecated        *string `json:"deprecated,omitempty"`
	Protected         bool    `json:"protected,omitempty"`
	DropCascade       bool    `json:"drop_cascade,omitempty"`
	RLSEnabled        bool    `json:"rls_enabled,omitempty"`
	RLSForced         bool    `json:"rls_forced,omitempty"`
	// ReplicaIdentityMode/ReplicaIdentityIndex mirror ir.ReplicaIdentity —
	// see Table.ReplicaIdentity's doc comment. Empty mode means "DEFAULT".
	ReplicaIdentityMode  string              `json:"replica_identity_mode,omitempty"`
	ReplicaIdentityIndex string              `json:"replica_identity_index,omitempty"`
	ClusterOn            *string             `json:"cluster_on,omitempty"`
	Inherits             []string            `json:"inherits,omitempty"`
	PartitionBy          string              `json:"partition_by,omitempty"` // e.g. "RANGE (created_at)"
	Partitions           []SnapPartition     `json:"partitions,omitempty"`
	Columns              []SnapColumn        `json:"columns,omitempty"`
	Constraints          []SnapConstraint    `json:"constraints,omitempty"`
	Indexes              []SnapIndex         `json:"indexes,omitempty"`
	Policies             []SnapPolicy        `json:"policies,omitempty"`
	Triggers             []SnapTrigger       `json:"triggers,omitempty"`
	Grants               []SnapGrant         `json:"grants,omitempty"`
	Revocations          []SnapGrant         `json:"revocations,omitempty"`
	SecurityLabels       []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps             []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

// SnapPartition is one partition entry attached to a partitioned table.
// PartitionBy/Partitions describe sub-partitioning (RFC Section 7.13): PartitionBy
// is "" for a leaf partition; when set, Partitions holds that partition's
// own nested partition entries, recursively.
type SnapPartition struct {
	Schema      string          `json:"schema,omitempty"`
	Name        string          `json:"name"`
	Bound       string          `json:"bound"` // raw FOR VALUES … expression
	PartitionBy string          `json:"partition_by,omitempty"`
	Partitions  []SnapPartition `json:"partitions,omitempty"`
	// Foreign, ForeignServer, and ForeignOptions mirror SnapTable's
	// identical fields (RFC Section 7.13's FOREIGN partition form),
	// including ForeignOptions' flattened comma-separated key=value form
	// (see SnapTable.ForeignOptions' doc comment).
	Foreign        bool    `json:"foreign,omitempty"`
	ForeignServer  *string `json:"foreign_server,omitempty"`
	ForeignOptions string  `json:"foreign_options,omitempty"`
	// Constraints mirrors ir.Partition.Constraints — constraints declared
	// directly on this partition, independent of the parent (RFC Section
	// 7.3's "DROP CONSTRAINT ... ONLY", PostgreSQL 18+).
	Constraints []SnapConstraint `json:"constraints,omitempty"`
}

type SnapColumn struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	NotNull  bool    `json:"not_null,omitempty"`
	Default  *string `json:"default,omitempty"`
	Identity *string `json:"identity,omitempty"` // "ALWAYS" or "BY DEFAULT"
	// Identity sequence options — mirror ir.Identity's identical fields,
	// nil/false meaning "not declared", same convention as SnapSequence's
	// own IncrementBy/MinValue/etc. below. Meaningless when Identity is nil.
	IdentityIncrementBy *int64  `json:"identity_increment_by,omitempty"`
	IdentityMinValue    *int64  `json:"identity_min_value,omitempty"`
	IdentityMaxValue    *int64  `json:"identity_max_value,omitempty"`
	IdentityNoMinValue  bool    `json:"identity_no_min_value,omitempty"`
	IdentityNoMaxValue  bool    `json:"identity_no_max_value,omitempty"`
	IdentityStartValue  *int64  `json:"identity_start_value,omitempty"`
	IdentityCache       *int64  `json:"identity_cache,omitempty"`
	IdentityCycle       *bool   `json:"identity_cycle,omitempty"`
	Serial              *string `json:"serial,omitempty"` // "SMALLSERIAL"/"SERIAL"/"BIGSERIAL"
	Generated           *string `json:"generated,omitempty"`
	// GeneratedVirtual is true when Generated is VIRTUAL (PG18+) rather
	// than STORED — meaningless when Generated is nil. Kept as a sibling
	// bool rather than folding into Generated's own shape, matching this
	// struct's existing flat-field convention (e.g. Identity/Serial are
	// each a bare *string, not a nested struct).
	GeneratedVirtual bool    `json:"generated_virtual,omitempty"`
	Comment          *string `json:"comment,omitempty"`
	Statistics       *int    `json:"statistics,omitempty"`
	Compression      *string `json:"compression,omitempty"`
	Storage          *string `json:"storage,omitempty"`
	Deprecated       *string `json:"deprecated,omitempty"`
	RenamedFrom      *string `json:"renamed_from,omitempty"`
	// Collation is a composite-type attribute's COLLATE target (RFC Section
	// 5.2); "" for a table column, which has no such feature.
	Collation      string              `json:"collation,omitempty"`
	Grants         []SnapGrant         `json:"grants,omitempty"`
	Revocations    []SnapGrant         `json:"revocations,omitempty"`
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

type SnapConstraint struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Expr       string `json:"expr,omitempty"`
	NotValid   bool   `json:"not_valid,omitempty"`
	Deferrable bool   `json:"deferrable,omitempty"`
	Comment    string `json:"comment,omitempty"`
	// NoInherit mirrors ir.Constraint.NoInherit (PostgreSQL 18+ NOT NULL
	// NO INHERIT, RFC Section 7.3).
	NoInherit bool `json:"no_inherit,omitempty"`
	// NotEnforced mirrors ir.Constraint.NotEnforced (PostgreSQL 18+ NOT
	// ENFORCED on CHECK/FOREIGN KEY, RFC Section 7.2/7.3).
	NotEnforced bool `json:"not_enforced,omitempty"`
	// InitiallyDeferred mirrors ir.Constraint.InitiallyDeferred — previously
	// tracked on the ir.Constraint side but never persisted to the
	// snapshot at all (Deferrable alone was), so a FOREIGN KEY's initial
	// deferred-vs-immediate state round-tripped incorrectly through
	// apply/re-plan.
	InitiallyDeferred bool `json:"initially_deferred,omitempty"`
}

// SnapIndex is a flat, fully comparable (string/bool fields only) snapshot of
// an index's definition. diff.diffIndexes builds one from the desired ir.Index
// via ToSnapIndex and compares it against the stored one with plain `==` to
// decide whether a same-named index needs to be recreated.
type SnapIndex struct {
	Name             string `json:"name"`
	Unique           bool   `json:"unique,omitempty"`
	Method           string `json:"method"`
	Columns          string `json:"columns"` // comma-separated; may carry ASC/DESC and NULLS FIRST/LAST suffixes
	Where            string `json:"where,omitempty"`
	Include          string `json:"include,omitempty"` // comma-separated covering columns
	NullsNotDistinct bool   `json:"nulls_not_distinct,omitempty"`
	With             string `json:"with,omitempty"` // comma-separated key=value storage params
	Tablespace       string `json:"tablespace,omitempty"`
	Comment          string `json:"comment,omitempty"`
}

type SnapPolicy struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	Permissive bool   `json:"permissive"`
	Using      string `json:"using,omitempty"`
	WithCheck  string `json:"with_check,omitempty"`
	// Roles is empty when the source never wrote a TO clause (PostgreSQL's
	// own default is then PUBLIC) — same nil-means-unspecified convention
	// used throughout this file. diffPolicies normalizes an empty list and
	// an explicit ["PUBLIC"] as equal, so this stays empty rather than
	// eagerly writing "PUBLIC" here.
	Roles   []string `json:"roles,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

type SnapTrigger struct {
	Name    string `json:"name"`
	When    string `json:"when"`
	Events  string `json:"events"` // comma-separated
	ForEach string `json:"for_each"`
	// UpdateOfColumns is RFC audit item #1's "UPDATE OF col1, col2, ..."
	// column list, comma-separated — see ir.Trigger.UpdateOfColumns' doc
	// comment.
	UpdateOfColumns string `json:"update_of_columns,omitempty"`
	// OldTransitionName/NewTransitionName are RFC Section 7.9's "REFERENCING OLD
	// TABLE AS ... NEW TABLE AS ..." transition-table names (audit item
	// #2) — "" means REFERENCING wasn't present for that side, same
	// empty-means-unspecified convention as Condition below.
	OldTransitionName string `json:"old_transition_name,omitempty"`
	NewTransitionName string `json:"new_transition_name,omitempty"`
	Function          string `json:"function"`
	Condition         string `json:"condition,omitempty"`
	Comment           string `json:"comment,omitempty"`
	// EnableState — see ir.Trigger.EnableState's identical doc comment.
	EnableState string `json:"enable_state,omitempty"`
	// DependsOnExtensions is Section 9.1's `[NO] DEPENDS ON EXTENSION`
	// diffing input reused for triggers (Section 7.9, audit item #75) —
	// see ir.Trigger.DependsOnExtensions' identical doc comment.
	DependsOnExtensions []string `json:"depends_on_extensions,omitempty"`
}

type SnapGrant struct {
	Privileges []string `json:"privileges,omitempty"` // nil = ALL
	Roles      []string `json:"roles"`
	WithGrant  bool     `json:"with_grant,omitempty"`
	// GrantedBy is RFC audit item #90's optional GRANTED BY role-spec
	// diffing input — see ir.Grant.GrantedBy's doc comment. Deliberately
	// excluded from grantKey (identity). Built from either side: on a
	// stored (desired-state) snapshot this exactly mirrors whatever was
	// last declared; on a live-introspected snapshot (see
	// snapshot.Populate) it's the real resolved grantor role name
	// (introspect*Grants captures pg_get_userbyid(a.grantor)), never a
	// symbolic role-spec keyword. diffGrantSet/diffRevocationSet's
	// grantedByMatches helper is what makes comparing these two different
	// possible shapes safe.
	GrantedBy *string `json:"granted_by,omitempty"`
}

// SnapSecurityLabel is one SECURITY LABEL entry (RFC Section 14.11) — the
// snapshot-side mirror of pipeline.SecurityLabel. Provider == "" is the
// unqualified form (resolves to the sole loaded provider at apply time).
type SnapSecurityLabel struct {
	Provider string `json:"provider,omitempty"`
	Label    string `json:"label"`
}

type SnapView struct {
	Schema     string  `json:"schema"`
	Name       string  `json:"name"`
	Query      string  `json:"query"`
	Owner      *string `json:"owner,omitempty"`
	Comment    *string `json:"comment,omitempty"`
	Deprecated *string `json:"deprecated,omitempty"`
	Recursive  bool    `json:"recursive,omitempty"`
	WithNoData bool    `json:"with_no_data,omitempty"`
	// Tablespace/StorageParams are Section 8.2's materialized-view
	// TABLESPACE/WITH (...) diffing inputs — see ir.View.Tablespace's doc
	// comment. StorageParams is flattened via flattenParams the same way
	// SnapTable.StorageParams is.
	Tablespace    *string     `json:"tablespace,omitempty"`
	StorageParams string      `json:"storage_params,omitempty"`
	Grants        []SnapGrant `json:"grants,omitempty"`
	Revocations   []SnapGrant `json:"revocations,omitempty"`
	// Indexes is only meaningful when Materialized (see ir.View.Indexes).
	Indexes        []SnapIndex         `json:"indexes,omitempty"`
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

type SnapFunction struct {
	Schema     string `json:"schema"`
	Name       string `json:"name"`
	Args       string `json:"args"` // type-only signature key
	ReturnType string `json:"return_type"`
	ReturnsSet bool   `json:"returns_set,omitempty"`
	// ReturnTable is the RETURNS TABLE(...) column list ("a integer, b text"),
	// empty for an ordinary function. ReturnType/ReturnsSet alone can't tell
	// two different TABLE column lists apart (both show "record"/true), so
	// this is compared independently — see ir.FormatTableColumns.
	ReturnTable    string              `json:"return_table,omitempty"`
	Language       string              `json:"language"`
	Volatility     string              `json:"volatility"`
	Parallel       string              `json:"parallel,omitempty"`
	Cost           *float64            `json:"cost,omitempty"`
	Rows           *float64            `json:"rows,omitempty"`
	BodyHash       string              `json:"body_hash"`
	Comment        *string             `json:"comment,omitempty"`
	Deprecated     *string             `json:"deprecated,omitempty"`
	Grants         []SnapGrant         `json:"grants,omitempty"`
	Revocations    []SnapGrant         `json:"revocations,omitempty"`
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
	// DependsOnExtensions is Section 9.1's `[NO] DEPENDS ON EXTENSION`
	// diffing input — see ir.Function.DependsOnExtensions' doc comment.
	DependsOnExtensions []string `json:"depends_on_extensions,omitempty"`
	// Leakproof is RFC audit item #25's `[NOT] LEAKPROOF` diffing input —
	// see ir.FuncAttrs.Leakproof's doc comment.
	Leakproof bool `json:"leakproof,omitempty"`
	// Transforms is RFC audit item #26's `TRANSFORM FOR TYPE` diffing
	// input, flattened to type-name strings — see ir.FuncAttrs.Transforms'
	// doc comment.
	Transforms []string `json:"transforms,omitempty"`
	// ObjFile/LinkSymbol are RFC audit item #27's `AS 'obj_file'[,
	// 'link_symbol']` diffing input — see ir.FuncAttrs.ObjFile's doc
	// comment.
	ObjFile    *string `json:"obj_file,omitempty"`
	LinkSymbol *string `json:"link_symbol,omitempty"`
	// AtomicBody is RFC audit item #28's `BEGIN ATOMIC ... END` diffing
	// input — see ir.FuncAttrs.AtomicBody's doc comment. Body content
	// itself is still carried by BodyHash; this only distinguishes the
	// recreate-time rendering form.
	AtomicBody bool `json:"atomic_body,omitempty"`
	// Owner is RFC audit item #70's ALTER FUNCTION ... OWNER TO diffing
	// input — see ir.Function.Owner's doc comment.
	Owner *string `json:"owner,omitempty"`
	// Strict/SecurityDef carry ir.FuncAttrs.Strict/SecurityDef into the
	// snapshot so an in-place STRICT/SECURITY DEFINER change on an
	// otherwise-unchanged function is detected as drift — see diffFunction's
	// recreate condition.
	Strict      bool `json:"strict,omitempty"`
	SecurityDef bool `json:"security_def,omitempty"`
	// Config is Section 9.2's SET-clause diffing input — see
	// ir.FuncAttrs.Config's doc comment. A change joins the same recreate
	// trigger as Cost/Rows/Leakproof; see diffFunction/diffProcedure.
	Config []SnapFuncConfig `json:"config,omitempty"`
}

// SnapFuncConfig is one FUNCTION/PROCEDURE-level SET clause's serialised
// form — see ir.FuncConfig's doc comment.
type SnapFuncConfig struct {
	Param       string  `json:"param"`
	Value       *string `json:"value,omitempty"`
	FromCurrent bool    `json:"from_current,omitempty"`
}

type SnapType struct {
	Schema         string       `json:"schema"`
	Name           string       `json:"name"`
	Variant        string       `json:"variant"`                   // ENUM, COMPOSITE, RANGE, DOMAIN, BASE
	Values         []string     `json:"values,omitempty"`          // ENUM only
	CompositeAttrs []SnapColumn `json:"composite_attrs,omitempty"` // COMPOSITE only
	BodyHash       string       `json:"body_hash,omitempty"`       // RANGE/BASE only; see sourceBodyHash
	// BaseStructured/BaseImmutableHash/BaseReceive/BaseSend/BaseTypmodIn/
	// BaseTypmodOut/BaseAnalyze/BaseSubscript/BaseStorage are BASE-only
	// (Section 5.5): structured diffing inputs for the 7 properties real
	// PostgreSQL's ALTER TYPE ... SET (...) can change in place, so a
	// change to just one of these gets a targeted ALTER instead of an
	// unconditional DROP+CREATE. BaseStructured is the same explicit-
	// sentinel pattern as OptionsStructured/CollationStructured elsewhere
	// in this file: false for any pre-existing snapshot saved before this
	// feature existed, so the differ falls back to the old whole-Body-hash
	// comparison for those rather than risking a spurious DROP+CREATE from
	// BaseImmutableHash using a different hash formula than the original
	// BodyHash. BaseImmutableHash hashes Body with these 7 properties'
	// "KEY = value" text stripped out first, so a change to one of them
	// alone doesn't also trip the immutable-property DESTRUCTIVE path.
	BaseStructured    bool    `json:"base_structured,omitempty"`
	BaseImmutableHash string  `json:"base_immutable_hash,omitempty"`
	BaseReceive       *string `json:"base_receive,omitempty"`
	BaseSend          *string `json:"base_send,omitempty"`
	BaseTypmodIn      *string `json:"base_typmod_in,omitempty"`
	BaseTypmodOut     *string `json:"base_typmod_out,omitempty"`
	BaseAnalyze       *string `json:"base_analyze,omitempty"`
	BaseSubscript     *string `json:"base_subscript,omitempty"`
	BaseStorage       *string `json:"base_storage,omitempty"`
	// MultirangeName is RANGE only (RFC Section 5.3): the range's multirange
	// companion type name, diffed separately from BodyHash so a change to it
	// alone is a rename rather than a rebuild.
	MultirangeName string `json:"multirange_name,omitempty"`
	// DomainBaseType/DomainDefault/DomainNotNull/DomainConstraints are
	// DOMAIN-only (RFC Section 5.4): structured diffing inputs, not just an opaque
	// body hash, so property-level changes get their own targeted ALTER
	// DOMAIN op instead of an unconditional DROP+CREATE.
	DomainBaseType string `json:"domain_base_type,omitempty"`
	// DomainCollation is RFC Section 5.4's "AS base_type [COLLATE
	// collation]" clause; "" means unspecified. Compared alongside
	// DomainBaseType since changing either requires DROP DOMAIN CASCADE +
	// CREATE DOMAIN, not a targeted ALTER.
	DomainCollation   string              `json:"domain_collation,omitempty"`
	DomainDefault     *string             `json:"domain_default,omitempty"`
	DomainNotNull     bool                `json:"domain_not_null,omitempty"`
	DomainConstraints []SnapConstraint    `json:"domain_constraints,omitempty"`
	Comment           *string             `json:"comment,omitempty"`
	Owner             *string             `json:"owner,omitempty"`
	Grants            []SnapGrant         `json:"grants,omitempty"`
	Revocations       []SnapGrant         `json:"revocations,omitempty"`
	SecurityLabels    []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps          []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

type SnapSequence struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// Unlogged is RFC Section 10's UNLOGGED prefix (ir.Sequence.Unlogged's
	// doc comment).
	Unlogged    bool        `json:"unlogged,omitempty"`
	Owner       *string     `json:"owner,omitempty"`
	Comment     *string     `json:"comment,omitempty"`
	Grants      []SnapGrant `json:"grants,omitempty"`
	Revocations []SnapGrant `json:"revocations,omitempty"`
	IncrementBy *int64      `json:"increment_by,omitempty"`
	MinValue    *int64      `json:"min_value,omitempty"`
	MaxValue    *int64      `json:"max_value,omitempty"`
	NoMinValue  bool        `json:"no_min_value,omitempty"`
	NoMaxValue  bool        `json:"no_max_value,omitempty"`
	StartValue  *int64      `json:"start_value,omitempty"`
	Cache       *int64      `json:"cache,omitempty"`
	Cycle       bool        `json:"cycle,omitempty"`
	// AsType/OwnedBy are RFC audit item #14's structured diffing inputs —
	// see ir.Sequence.AsType/OwnedBy's doc comments. AsType is "" when
	// never declared (a real sequence's resolved type is never empty, so
	// this doubles as the same self-healing "stale snapshot" signal
	// already used elsewhere, e.g. diffType's DOMAIN branch).
	AsType         string              `json:"as_type,omitempty"`
	OwnedBy        *string             `json:"owned_by,omitempty"`
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

// SnapRole is a Role's stored state (RFC Section 11.1). PasswordHash is a hash of
// the *declared* PASSWORD text (literal or {{secret-uri}} reference,
// verbatim) — never the resolved value; see ir.Role's doc comment and RFC
// Section 11.1's "Password drift detection" for why hashing the declared text
// (not just a boolean has_password) is safe and enables rotation detection.
type SnapRole struct {
	Name            string  `json:"name"`
	CanLogin        *bool   `json:"can_login,omitempty"`
	Superuser       *bool   `json:"superuser,omitempty"`
	CreateDB        *bool   `json:"create_db,omitempty"`
	CreateRole      *bool   `json:"create_role,omitempty"`
	Inherit         *bool   `json:"inherit,omitempty"`
	IsReplication   *bool   `json:"is_replication,omitempty"`
	BypassRLS       *bool   `json:"bypass_rls,omitempty"`
	ConnectionLimit *int    `json:"connection_limit,omitempty"`
	PasswordHash    string  `json:"password_hash,omitempty"`
	ValidUntil      *string `json:"valid_until,omitempty"`
	Comment         *string `json:"comment,omitempty"`
	// Memberships is RFC audit item #32's unified IN ROLE/ROLE/ADMIN
	// diffing input — see ir.RoleMembership's doc comment.
	Memberships []SnapRoleMembership `json:"memberships,omitempty"`
	// Configs is RFC audit item #74's role-config-dir diffing input — see
	// ir.RoleConfig's doc comment.
	Configs        []SnapRoleConfig    `json:"configs,omitempty"`
	SecurityLabels []SnapSecurityLabel `json:"security_labels,omitempty"`
	NameMaps       []SnapNameMapEntry  `json:"name_maps,omitempty"`
}

// SnapRoleMembership is the snapshot form of ir.RoleMembership.
type SnapRoleMembership struct {
	Role      string `json:"role"`
	Direction string `json:"direction"`
	Admin     bool   `json:"admin,omitempty"`
	Inherit   *bool  `json:"inherit,omitempty"`
	Set       *bool  `json:"set,omitempty"`
}

// SnapRoleConfig is the snapshot form of ir.RoleConfig.
type SnapRoleConfig struct {
	Param       string  `json:"param,omitempty"`
	Value       *string `json:"value,omitempty"`
	FromCurrent bool    `json:"from_current,omitempty"`
	Reset       bool    `json:"reset,omitempty"`
	ResetAll    bool    `json:"reset_all,omitempty"`
	InDatabase  *string `json:"in_database,omitempty"`
}

// SnapVtypeBody is the serialised form of an ir.VtypeBody discriminated union.
// Kind is one of "type_ref", "composite", or "union".
type SnapVtypeBody struct {
	Kind string `json:"kind"`
	// type_ref fields
	Schema  string `json:"schema,omitempty"`
	Name    string `json:"name,omitempty"`
	IsArray bool   `json:"array,omitempty"`
	// composite fields
	Fields []SnapVtypeField `json:"fields,omitempty"`
	// union fields
	Members []SnapVtypeBody `json:"members,omitempty"`
}

// SnapVtypeField is one named field inside a SnapVtypeBody composite.
type SnapVtypeField struct {
	Name string        `json:"name"`
	Type SnapVtypeBody `json:"type"`
}

// SnapVirtualType is the snapshot form of a VIRTUAL TYPE declaration.
// Columns and composite type attributes may reference a virtual type; DPG
// resolves those to jsonb / jsonb[] in generated SQL.  The structured body is
// stored here for downstream consumers (ORMs, type-safe query builders).
type SnapVirtualType struct {
	Schema     string             `json:"schema,omitempty"`
	Name       string             `json:"name"`
	Body       SnapVtypeBody      `json:"body"`
	JsonFormat string             `json:"json_format,omitempty"` // "json" or "jsonb"; empty = default (jsonb)
	Comment    *string            `json:"comment,omitempty"`
	NameMaps   []SnapNameMapEntry `json:"name_maps,omitempty"`
}

// SnapDefaultPrivileges is the snapshot form of a DEFAULT PRIVILEGES declaration.
// Each entry identifies a (ForRole, InSchema, ObjectType) combination and its grants.
type SnapDefaultPrivileges struct {
	ForRole     *string     `json:"for_role,omitempty"`
	InSchema    *string     `json:"in_schema,omitempty"`
	ObjectType  string      `json:"object_type"`
	Grants      []SnapGrant `json:"grants,omitempty"`
	Revocations []SnapGrant `json:"revocations,omitempty"`
}

// SnapParameterPrivileges is the snapshot form of a PARAMETER PRIVILEGES
// declaration (RFC Section 11.6, PG15+). Cluster-scoped singleton — one per
// project, unlike SnapDefaultPrivileges which splits per (role, schema,
// object type).
type SnapParameterPrivileges struct {
	Grants      []SnapParamGrant `json:"grants,omitempty"`
	Revocations []SnapParamGrant `json:"revocations,omitempty"`
}

// SnapParamGrant is one grant/revocation entry within a PARAMETER PRIVILEGES
// declaration. A dedicated type rather than reusing SnapGrant: a PARAMETER
// PRIVILEGES grant targets a set of named parameters, unlike every other
// GRANTS block where the object is either implicit (a single enclosing
// object) or a single ON <type> keyword (SnapDefaultPrivileges) — Parameters
// plays that role here. Cascade is intentionally not stored, matching
// toSnapRevocation's identical DefaultPrivileges precedent (a revocation is
// matched for diffing purposes by privileges+parameters+roles only).
type SnapParamGrant struct {
	Privileges []string `json:"privileges,omitempty"` // nil = ALL
	Parameters []string `json:"parameters"`
	Roles      []string `json:"roles"`
	WithGrant  bool     `json:"with_grant,omitempty"`
	// GrantedBy — see SnapGrant.GrantedBy's identical doc comment. Excluded
	// from paramGrantKey (identity), same "declared, so managed" comparison
	// rule via grantedByMatches.
	GrantedBy *string `json:"granted_by,omitempty"`
}
