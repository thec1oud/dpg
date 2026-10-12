package ir

import (
	"github.com/thec1oud/dpg/internal/pipeline"
)

// Block directive names, as reported by DPG-E024.
const (
	dirComment          = "COMMENT"
	dirOwner            = "OWNER"
	dirRenamedFrom      = "RENAMED FROM"
	dirProtected        = "PROTECTED"
	dirDeprecated       = "DEPRECATED"
	dirDropCascade      = "DROP CASCADE"
	dirIndexes          = "INDEXES"
	dirPolicies         = "POLICIES"
	dirTriggers         = "TRIGGERS"
	dirGrants           = "GRANTS"
	dirRevokes          = "REVOKES"
	dirSecurityLabel    = "SECURITY LABEL"
	dirColumn           = "COLUMN"
	dirAttribute        = "ATTRIBUTE"
	dirConstraint       = "CONSTRAINT"
	dirEnableRLS        = "ENABLE ROW LEVEL SECURITY"
	dirForceRLS         = "FORCE ROW LEVEL SECURITY"
	dirPartitions       = "PARTITIONS"
	dirMigrateRemove    = "MIGRATE REMOVE"
	dirDefaultPrivs     = "DEFAULT PRIVILEGES"
	dirMapping          = "MAPPING"
	dirFamilyMembers    = "OPERATOR/FUNCTION"
	dirPreferredJSON    = "PREFERRED JSON FORMAT"
	dirNameMap          = "NAME MAP"
	dirDomainDefault    = "DEFAULT"
	dirDomainNotNull    = "NOT NULL"
	dirDependsOnExt     = "DEPENDS ON EXTENSION"
	dirReplicaIdentity  = "REPLICA IDENTITY"
	dirClusterOn        = "CLUSTER ON"
	dirRefreshVersion   = "REFRESH VERSION"
	dirDetachedFrom     = "DETACHED FROM"
	dirValueRenamedFrom = "VALUE ... RENAMED FROM"
	dirEnableState      = "ENABLE/DISABLE"
	dirRoleConfig       = "SET/RESET"
	dirMembership       = "IN ROLE/ROLE/ADMIN"
)

type blockDirective struct {
	name string
	pos  pipeline.SourcePos
}

// blockDirectives lists the directives a parsed block declares.
func blockDirectives(b pipeline.BlockAST) []blockDirective {
	var out []blockDirective
	add := func(present bool, name string, pos pipeline.SourcePos) {
		if present {
			if pos.Line == 0 {
				pos = b.Pos
			}
			out = append(out, blockDirective{name, pos})
		}
	}
	first := func(n int) bool { return n > 0 }
	add(b.Comment != nil, dirComment, pipeline.SourcePos{})
	add(b.Owner != nil, dirOwner, pipeline.SourcePos{})
	add(b.RenamedFrom != nil, dirRenamedFrom, pipeline.SourcePos{})
	add(b.Protected, dirProtected, pipeline.SourcePos{})
	add(b.Deprecated != nil, dirDeprecated, pipeline.SourcePos{})
	add(b.DropCascade, dirDropCascade, pipeline.SourcePos{})
	add(first(len(b.Indices)), dirIndexes, pipeline.SourcePos{})
	add(first(len(b.Policies)), dirPolicies, pipeline.SourcePos{})
	add(first(len(b.Triggers)), dirTriggers, pipeline.SourcePos{})
	add(first(len(b.Grants)), dirGrants, pipeline.SourcePos{})
	add(first(len(b.Revocations)), dirRevokes, pipeline.SourcePos{})
	add(first(len(b.SecurityLabels)), dirSecurityLabel, pipeline.SourcePos{})
	add(first(len(b.Columns)), dirColumn, pipeline.SourcePos{})
	add(first(len(b.Attributes)), dirAttribute, pipeline.SourcePos{})
	add(first(len(b.Constraints)), dirConstraint, pipeline.SourcePos{})
	add(b.EnableRLS, dirEnableRLS, pipeline.SourcePos{})
	add(b.ForceRLS, dirForceRLS, pipeline.SourcePos{})
	add(b.Partitions != nil, dirPartitions, pipeline.SourcePos{})
	add(b.MigrateRemove != nil, dirMigrateRemove, pipeline.SourcePos{})
	add(first(len(b.DefaultPrivileges)), dirDefaultPrivs, pipeline.SourcePos{})
	add(first(len(b.Mappings)), dirMapping, pipeline.SourcePos{})
	add(first(len(b.OpFamilyMembers)), dirFamilyMembers, pipeline.SourcePos{})
	add(b.PreferredJsonFormat != "", dirPreferredJSON, pipeline.SourcePos{})
	add(first(len(b.NameMaps)), dirNameMap, pipeline.SourcePos{})
	add(b.DomainDefault != nil, dirDomainDefault, pipeline.SourcePos{})
	add(b.DomainNotNull, dirDomainNotNull, pipeline.SourcePos{})
	add(first(len(b.DependsOnExtensions)), dirDependsOnExt, pipeline.SourcePos{})
	add(b.ReplicaIdentity != nil, dirReplicaIdentity, pipeline.SourcePos{})
	add(b.ClusterOn != nil, dirClusterOn, pipeline.SourcePos{})
	add(b.RefreshVersion, dirRefreshVersion, pipeline.SourcePos{})
	add(b.DetachedFrom != nil, dirDetachedFrom, pipeline.SourcePos{})
	add(first(len(b.EnumValueRenames)), dirValueRenamedFrom, pipeline.SourcePos{})
	add(b.TriggerEnableState != "", dirEnableState, pipeline.SourcePos{})
	add(first(len(b.RoleConfigs)), dirRoleConfig, pipeline.SourcePos{})
	add(first(len(b.Memberships)), dirMembership, pipeline.SourcePos{})
	return out
}

func directiveSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Directive sets shared by several kinds (RFC Section 4.2.1).
var (
	typeDirectives = []string{dirComment, dirOwner, dirRenamedFrom, dirGrants, dirRevokes, dirSecurityLabel, dirNameMap}
	routineDirs    = []string{dirComment, dirOwner, dirRenamedFrom, dirDeprecated, dirGrants, dirRevokes, dirSecurityLabel, dirNameMap}
)

// allowedDirectives returns the directives obj's block accepts (each
// object section of the RFC lists them), and the kind's name for the
// error message. ok is false for kinds this check doesn't cover.
func allowedDirectives(obj pipeline.IRObject) (allowed map[string]bool, kind string, ok bool) {
	switch o := obj.(type) {
	case *Table:
		kind = "TABLE"
		if o.Foreign {
			kind = "FOREIGN TABLE"
		}
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirProtected, dirDeprecated, dirDropCascade,
			dirIndexes, dirPolicies, dirTriggers, dirGrants, dirRevokes, dirSecurityLabel, dirColumn,
			dirConstraint, dirEnableRLS, dirForceRLS, dirPartitions, dirNameMap, dirReplicaIdentity,
			dirClusterOn, dirDetachedFrom), kind, true
	case *View:
		set := directiveSet(dirComment, dirOwner, dirRenamedFrom, dirDeprecated, dirGrants, dirRevokes,
			dirSecurityLabel, dirNameMap)
		if o.Materialized {
			set[dirIndexes] = true
			return set, "MATERIALIZED VIEW", true
		}
		return set, "VIEW", true
	case *Function:
		return directiveSet(append(routineDirs, dirDependsOnExt)...), "FUNCTION", true
	case *Procedure:
		return directiveSet(append(routineDirs, dirDependsOnExt)...), "PROCEDURE", true
	case *Aggregate:
		return directiveSet(routineDirs...), "AGGREGATE", true
	case *Type:
		switch o.Variant {
		case "ENUM":
			return directiveSet(append(typeDirectives, dirDeprecated, dirValueRenamedFrom, dirMigrateRemove)...), "TYPE ... AS ENUM", true
		case "COMPOSITE":
			return directiveSet(append(typeDirectives, dirDeprecated, dirAttribute)...), "composite TYPE", true
		case "DOMAIN":
			return directiveSet(append(typeDirectives, dirConstraint, dirDomainDefault, dirDomainNotNull)...), "DOMAIN", true
		default:
			return directiveSet(typeDirectives...), "TYPE", true
		}
	case *Schema:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirGrants, dirRevokes, dirSecurityLabel,
			dirNameMap, dirDefaultPrivs), "SCHEMA", true
	case *Extension:
		return directiveSet(dirComment, dirNameMap), "EXTENSION", true
	case *Sequence:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirGrants, dirRevokes, dirSecurityLabel,
			dirNameMap), "SEQUENCE", true
	case *Role:
		return directiveSet(dirComment, dirRenamedFrom, dirSecurityLabel, dirNameMap, dirRoleConfig,
			dirMembership), "ROLE", true
	case *Tablespace:
		return directiveSet(dirComment, dirRenamedFrom, dirGrants, dirRevokes, dirSecurityLabel), "TABLESPACE", true
	case *ForeignDataWrapper:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirGrants, dirRevokes), "FOREIGN DATA WRAPPER", true
	case *ForeignServer:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirGrants, dirRevokes), "SERVER", true
	case *UserMapping:
		return directiveSet(dirComment), "USER MAPPING", true
	case *StatisticsObject:
		return directiveSet(dirComment, dirRenamedFrom), "STATISTICS", true
	case *Operator:
		return directiveSet(dirComment), "OPERATOR", true
	case *Collation:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirRefreshVersion), "COLLATION", true
	case *TSConfig:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirMapping), "TEXT SEARCH CONFIGURATION", true
	case *TSDict:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom), "TEXT SEARCH DICTIONARY", true
	case *TSParser:
		return directiveSet(dirComment, dirRenamedFrom), "TEXT SEARCH PARSER", true
	case *TSTemplate:
		return directiveSet(dirComment, dirRenamedFrom), "TEXT SEARCH TEMPLATE", true
	case *Cast:
		return directiveSet(dirComment), "CAST", true
	case *Subscription:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirSecurityLabel), "SUBSCRIPTION", true
	case *Publication:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirSecurityLabel), "PUBLICATION", true
	case *EventTrigger:
		return directiveSet(dirComment, dirOwner, dirRenamedFrom, dirSecurityLabel, dirEnableState), "EVENT TRIGGER", true
	case *OperatorClass:
		return directiveSet(dirComment, dirRenamedFrom), "OPERATOR CLASS", true
	case *OperatorFamily:
		return directiveSet(dirComment, dirRenamedFrom, dirFamilyMembers), "OPERATOR FAMILY", true
	}
	return nil, "", false
}

// checkBlockDirectives reports DPG-E024 for the first directive in block
// that obj's kind does not accept. A directive the kind doesn't support
// would otherwise be silently ignored.
func checkBlockDirectives(obj pipeline.IRObject, block pipeline.BlockAST) error {
	allowed, kind, ok := allowedDirectives(obj)
	if !ok {
		return nil
	}
	for _, d := range blockDirectives(block) {
		if !allowed[d.name] {
			return pipeline.ErrorfCode(d.pos, "DPG-E024",
				"%s is not accepted in a { } block of %s", d.name, kind)
		}
	}
	return nil
}
