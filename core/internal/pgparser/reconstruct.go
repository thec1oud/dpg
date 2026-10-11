package pgparser

import (
	"github.com/thec1oud/dpg/internal/pipeline"
)

// Reconstruct prepends the correct CREATE verb to a stripped Part1 string,
// producing a valid PG DDL statement that can be passed to pg_query.Parse.
// The scanner emits Part1 without the leading DPG keyword(s); this restores them.
func Reconstruct(kind pipeline.ObjectKind, part1 string) string {
	switch kind {
	case pipeline.KindTable:
		return "CREATE TABLE " + part1
	case pipeline.KindUnloggedTable:
		return "CREATE UNLOGGED TABLE " + part1
	case pipeline.KindForeignTable:
		return "CREATE FOREIGN TABLE " + part1
	case pipeline.KindView:
		return "CREATE VIEW " + part1
	case pipeline.KindMaterializedView:
		return "CREATE MATERIALIZED VIEW " + part1
	case pipeline.KindRecursiveView:
		return "CREATE RECURSIVE VIEW " + part1
	case pipeline.KindFunction:
		return "CREATE FUNCTION " + part1
	case pipeline.KindProcedure:
		return "CREATE PROCEDURE " + part1
	case pipeline.KindAggregate:
		return "CREATE AGGREGATE " + part1
	case pipeline.KindEnum:
		return "CREATE TYPE " + part1
	case pipeline.KindCompositeType:
		return "CREATE TYPE " + part1
	case pipeline.KindRangeType:
		return "CREATE TYPE " + part1
	case pipeline.KindDomainType:
		return "CREATE DOMAIN " + part1
	case pipeline.KindBaseType:
		return "CREATE TYPE " + part1
	case pipeline.KindSchema:
		return "CREATE SCHEMA " + part1
	case pipeline.KindExtension:
		return "CREATE EXTENSION " + part1
	case pipeline.KindSequence:
		return "CREATE SEQUENCE " + part1
	case pipeline.KindUnloggedSequence:
		return "CREATE UNLOGGED SEQUENCE " + part1
	case pipeline.KindRole:
		return "CREATE ROLE " + part1
	case pipeline.KindTablespace:
		return "CREATE TABLESPACE " + part1
	case pipeline.KindFDW:
		return "CREATE FOREIGN DATA WRAPPER " + part1
	case pipeline.KindServer:
		return "CREATE SERVER " + part1
	case pipeline.KindUserMapping:
		return "CREATE USER MAPPING " + part1
	case pipeline.KindPublication:
		return "CREATE PUBLICATION " + part1
	case pipeline.KindSubscription:
		return "CREATE SUBSCRIPTION " + part1
	case pipeline.KindEventTrigger:
		return "CREATE EVENT TRIGGER " + part1
	case pipeline.KindCollation:
		return "CREATE COLLATION " + part1
	case pipeline.KindOperator:
		return "CREATE OPERATOR " + part1
	case pipeline.KindOperatorClass:
		return "CREATE OPERATOR CLASS " + part1
	case pipeline.KindOperatorFamily:
		return "CREATE OPERATOR FAMILY " + part1
	case pipeline.KindCast:
		return "CREATE CAST " + part1
	case pipeline.KindStatisticsObject:
		return "CREATE STATISTICS " + part1
	case pipeline.KindTSConfig:
		return "CREATE TEXT SEARCH CONFIGURATION " + part1
	case pipeline.KindTSDict:
		return "CREATE TEXT SEARCH DICTIONARY " + part1
	case pipeline.KindTSParser:
		return "CREATE TEXT SEARCH PARSER " + part1
	case pipeline.KindTSTemplate:
		return "CREATE TEXT SEARCH TEMPLATE " + part1
	default:
		return part1
	}
}
