package dev.thec1oud.plugins.ide.dpg.lang

import dev.thec1oud.plugins.ide.dpg.DpgLanguage
import com.intellij.psi.TokenType
import com.intellij.psi.tree.IElementType
import com.intellij.psi.tree.TokenSet

class DpgTokenType(debugName: String) : IElementType(debugName, DpgLanguage)

@Suppress("MemberVisibilityCanBePrivate")
object DpgTokenTypes {
    // ── Whitespace / bad ─────────────────────────────────────────────────────
    @JvmField val WHITE_SPACE    = TokenType.WHITE_SPACE
    @JvmField val BAD_CHARACTER  = TokenType.BAD_CHARACTER

    // ── Comments ──────────────────────────────────────────────────────────────
    @JvmField val LINE_COMMENT   = DpgTokenType("LINE_COMMENT")
    @JvmField val BLOCK_COMMENT  = DpgTokenType("BLOCK_COMMENT")

    // ── Punctuation ───────────────────────────────────────────────────────────
    @JvmField val LBRACE      = DpgTokenType("LBRACE")       // {
    @JvmField val RBRACE      = DpgTokenType("RBRACE")       // }
    @JvmField val LPAREN      = DpgTokenType("LPAREN")       // (
    @JvmField val RPAREN      = DpgTokenType("RPAREN")       // )
    @JvmField val SEMICOLON   = DpgTokenType("SEMICOLON")    // ;
    @JvmField val COMMA       = DpgTokenType("COMMA")        // ,
    @JvmField val DOT         = DpgTokenType("DOT")          // .
    @JvmField val SPREAD      = DpgTokenType("SPREAD")       // ...
    @JvmField val COLON_COLON = DpgTokenType("COLON_COLON")  // ::
    @JvmField val COLON       = DpgTokenType("COLON")        // :
    @JvmField val EQ          = DpgTokenType("EQ")           // =
    @JvmField val STAR        = DpgTokenType("STAR")         // *
    @JvmField val OPERATOR    = DpgTokenType("OPERATOR")     // other operator chars

    // ── Literals ──────────────────────────────────────────────────────────────
    @JvmField val STRING_LITERAL       = DpgTokenType("STRING_LITERAL")
    @JvmField val DOLLAR_QUOTE         = DpgTokenType("DOLLAR_QUOTE")         // $$  /  $tag$
    @JvmField val DOLLAR_QUOTE_CONTENT = DpgTokenType("DOLLAR_QUOTE_CONTENT")
    @JvmField val IDENTIFIER           = DpgTokenType("IDENTIFIER")
    @JvmField val INTEGER              = DpgTokenType("INTEGER")

    // ── DPG object-type keywords (start a top-level declaration) ─────────────
    @JvmField val SCHEMA_KW       = DpgTokenType("SCHEMA_KW")
    @JvmField val TABLE_KW        = DpgTokenType("TABLE_KW")
    @JvmField val VIEW_KW         = DpgTokenType("VIEW_KW")
    @JvmField val FUNCTION_KW     = DpgTokenType("FUNCTION_KW")
    @JvmField val PROCEDURE_KW    = DpgTokenType("PROCEDURE_KW")
    @JvmField val AGGREGATE_KW    = DpgTokenType("AGGREGATE_KW")
    @JvmField val TYPE_KW         = DpgTokenType("TYPE_KW")
    @JvmField val DOMAIN_KW       = DpgTokenType("DOMAIN_KW")
    @JvmField val SEQUENCE_KW     = DpgTokenType("SEQUENCE_KW")
    @JvmField val ROLE_KW         = DpgTokenType("ROLE_KW")
    @JvmField val TABLESPACE_KW   = DpgTokenType("TABLESPACE_KW")
    @JvmField val EXTENSION_KW    = DpgTokenType("EXTENSION_KW")
    @JvmField val MACRO_KW        = DpgTokenType("MACRO_KW")
    @JvmField val PUBLICATION_KW  = DpgTokenType("PUBLICATION_KW")
    @JvmField val SUBSCRIPTION_KW = DpgTokenType("SUBSCRIPTION_KW")
    @JvmField val COLLATION_KW    = DpgTokenType("COLLATION_KW")
    @JvmField val OPERATOR_KW     = DpgTokenType("OPERATOR_KW")
    @JvmField val CAST_KW         = DpgTokenType("CAST_KW")
    @JvmField val SERVER_KW       = DpgTokenType("SERVER_KW")

    // ── Multi-word leading keywords ───────────────────────────────────────────
    @JvmField val MATERIALIZED_KW = DpgTokenType("MATERIALIZED_KW")  // MATERIALIZED VIEW
    @JvmField val RECURSIVE_KW    = DpgTokenType("RECURSIVE_KW")     // RECURSIVE VIEW
    @JvmField val UNLOGGED_KW     = DpgTokenType("UNLOGGED_KW")      // UNLOGGED TABLE
    @JvmField val FOREIGN_KW      = DpgTokenType("FOREIGN_KW")       // FOREIGN TABLE / DATA WRAPPER
    @JvmField val EVENT_KW        = DpgTokenType("EVENT_KW")         // EVENT TRIGGER
    @JvmField val TEXT_KW         = DpgTokenType("TEXT_KW")          // TEXT SEARCH ...
    @JvmField val DEFAULT_KW      = DpgTokenType("DEFAULT_KW")       // DEFAULT PRIVILEGES
    @JvmField val VIRTUAL_KW      = DpgTokenType("VIRTUAL_KW")       // VIRTUAL TYPE
    @JvmField val USER_KW         = DpgTokenType("USER_KW")          // USER MAPPING
    @JvmField val DATA_KW         = DpgTokenType("DATA_KW")          // DATA (in FOREIGN DATA WRAPPER)

    // ── Block directive keywords (inside { } blocks) ──────────────────────────
    @JvmField val INDEX_KW        = DpgTokenType("INDEX_KW")
    @JvmField val INDEXES_KW      = DpgTokenType("INDEXES_KW")
    @JvmField val POLICY_KW       = DpgTokenType("POLICY_KW")
    @JvmField val POLICIES_KW     = DpgTokenType("POLICIES_KW")
    @JvmField val TRIGGER_KW      = DpgTokenType("TRIGGER_KW")
    @JvmField val TRIGGERS_KW     = DpgTokenType("TRIGGERS_KW")
    @JvmField val GRANT_KW        = DpgTokenType("GRANT_KW")
    @JvmField val GRANTS_KW       = DpgTokenType("GRANTS_KW")
    @JvmField val REVOKE_KW   = DpgTokenType("REVOKE_KW")
    @JvmField val REVOKES_KW  = DpgTokenType("REVOKES_KW")
    @JvmField val PARTITION_KW    = DpgTokenType("PARTITION_KW")
    @JvmField val PARTITIONS_KW   = DpgTokenType("PARTITIONS_KW")
    @JvmField val COLUMN_KW       = DpgTokenType("COLUMN_KW")
    @JvmField val COLUMNS_KW      = DpgTokenType("COLUMNS_KW")
    @JvmField val ATTRIBUTE_KW    = DpgTokenType("ATTRIBUTE_KW")
    @JvmField val ATTRIBUTES_KW   = DpgTokenType("ATTRIBUTES_KW")
    @JvmField val CONSTRAINT_KW   = DpgTokenType("CONSTRAINT_KW")
    @JvmField val CONSTRAINTS_KW  = DpgTokenType("CONSTRAINTS_KW")
    @JvmField val COMMENT_KW      = DpgTokenType("COMMENT_KW")
    @JvmField val OWNER_KW        = DpgTokenType("OWNER_KW")
    @JvmField val PROTECTED_KW    = DpgTokenType("PROTECTED_KW")
    @JvmField val DEPRECATED_KW   = DpgTokenType("DEPRECATED_KW")
    @JvmField val RENAMED_KW      = DpgTokenType("RENAMED_KW")
    @JvmField val MIGRATE_KW      = DpgTokenType("MIGRATE_KW")
    @JvmField val ENABLE_KW       = DpgTokenType("ENABLE_KW")
    @JvmField val DISABLE_KW      = DpgTokenType("DISABLE_KW")
    @JvmField val FORCE_KW        = DpgTokenType("FORCE_KW")
    @JvmField val NOFORCE_KW      = DpgTokenType("NOFORCE_KW")
    @JvmField val DROP_KW         = DpgTokenType("DROP_KW")         // DROP CASCADE directive
    @JvmField val STATISTICS_KW   = DpgTokenType("STATISTICS_KW")   // STATISTICS n directive

    // ── Role attribute keywords ───────────────────────────────────────────────
    @JvmField val LOGIN_KW        = DpgTokenType("LOGIN_KW")
    @JvmField val NOLOGIN_KW      = DpgTokenType("NOLOGIN_KW")
    @JvmField val SUPERUSER_KW    = DpgTokenType("SUPERUSER_KW")
    @JvmField val NOSUPERUSER_KW  = DpgTokenType("NOSUPERUSER_KW")
    @JvmField val CREATEDB_KW     = DpgTokenType("CREATEDB_KW")
    @JvmField val NOCREATEDB_KW   = DpgTokenType("NOCREATEDB_KW")
    @JvmField val CREATEROLE_KW   = DpgTokenType("CREATEROLE_KW")
    @JvmField val NOCREATEROLE_KW = DpgTokenType("NOCREATEROLE_KW")
    @JvmField val INHERIT_KW      = DpgTokenType("INHERIT_KW")
    @JvmField val NOINHERIT_KW    = DpgTokenType("NOINHERIT_KW")
    @JvmField val REPLICATION_KW  = DpgTokenType("REPLICATION_KW")
    @JvmField val NOREPLICATION_KW= DpgTokenType("NOREPLICATION_KW")
    @JvmField val BYPASSRLS_KW    = DpgTokenType("BYPASSRLS_KW")
    @JvmField val NOBYPASSRLS_KW  = DpgTokenType("NOBYPASSRLS_KW")
    @JvmField val PASSWORD_KW     = DpgTokenType("PASSWORD_KW")
    @JvmField val CONNECTION_KW   = DpgTokenType("CONNECTION_KW")
    @JvmField val VALID_KW        = DpgTokenType("VALID_KW")
    @JvmField val UNTIL_KW        = DpgTokenType("UNTIL_KW")
    @JvmField val IN_KW           = DpgTokenType("IN_KW")

    // ── Forbidden top-level verbs (DPG-E006) ─────────────────────────────────
    @JvmField val FORBIDDEN_VERB = DpgTokenType("FORBIDDEN_VERB")  // CREATE, ALTER

    // ── General SQL keyword (appears in part-1 bodies) ────────────────────────
    @JvmField val SQL_KW = DpgTokenType("SQL_KW")

    // ── TokenSet helpers ──────────────────────────────────────────────────────
    @JvmField val COMMENTS = TokenSet.create(LINE_COMMENT, BLOCK_COMMENT)

    @JvmField val STRINGS = TokenSet.create(STRING_LITERAL, DOLLAR_QUOTE, DOLLAR_QUOTE_CONTENT)

    // MACRO_KW is excluded: it is handled by parseMacroDeclaration, not parseObjectDeclaration,
    // and has its own MACRO_KEYWORD highlight colour.
    // STATISTICS_KW is excluded: it is overwhelmingly used as a block directive
    // (STATISTICS n target) so it lives only in BLOCK_DIRECTIVE_KEYWORDS.
    @JvmField val DPG_OBJECT_KEYWORDS: TokenSet = TokenSet.create(
        SCHEMA_KW, TABLE_KW, VIEW_KW, FUNCTION_KW, PROCEDURE_KW,
        AGGREGATE_KW, TYPE_KW, DOMAIN_KW, SEQUENCE_KW,
        ROLE_KW, TABLESPACE_KW, EXTENSION_KW,
        PUBLICATION_KW, SUBSCRIPTION_KW, COLLATION_KW, OPERATOR_KW,
        CAST_KW, SERVER_KW,
        MATERIALIZED_KW, RECURSIVE_KW, UNLOGGED_KW, FOREIGN_KW,
        EVENT_KW, TEXT_KW, DEFAULT_KW, VIRTUAL_KW, USER_KW
    )

    // OPERATOR_KW/FUNCTION_KW deliberately belong to BOTH this set and
    // DPG_OBJECT_KEYWORDS above (unlike STATISTICS_KW, which was moved here
    // exclusively) — they're common, load-bearing top-level object keywords
    // (OPERATOR ===(...), FUNCTION foo(...)) that must keep parsing there,
    // AND, since RFC §14.4, also valid inside an OPERATOR FAMILY { } block
    // as loose-member directives (OPERATOR 1 <(...), FUNCTION 1 (...) fn(...)).
    // Dual TokenSet membership is safe here: parseObjectDeclaration's
    // isObjectKeyword() check and parseBlockContent's isBlockDirectiveKw()
    // check run in disjoint parsing contexts (top-level source-file loop vs.
    // already-inside-a-{ }-block loop), so there is no dispatch ambiguity.
    @JvmField val BLOCK_DIRECTIVE_KEYWORDS: TokenSet = TokenSet.create(
        INDEX_KW, INDEXES_KW, POLICY_KW, POLICIES_KW,
        TRIGGER_KW, TRIGGERS_KW, GRANT_KW, GRANTS_KW,
        REVOKE_KW, REVOKES_KW, PARTITION_KW, PARTITIONS_KW,
        COLUMN_KW, COLUMNS_KW, ATTRIBUTE_KW, ATTRIBUTES_KW, CONSTRAINT_KW, CONSTRAINTS_KW,
        COMMENT_KW, OWNER_KW, PROTECTED_KW, DEPRECATED_KW,
        RENAMED_KW, MIGRATE_KW, ENABLE_KW, DISABLE_KW, FORCE_KW,
        NOFORCE_KW, DROP_KW, STATISTICS_KW,
        LOGIN_KW, NOLOGIN_KW, SUPERUSER_KW, NOSUPERUSER_KW,
        CREATEDB_KW, NOCREATEDB_KW, CREATEROLE_KW, NOCREATEROLE_KW,
        INHERIT_KW, NOINHERIT_KW, REPLICATION_KW, NOREPLICATION_KW,
        BYPASSRLS_KW, NOBYPASSRLS_KW, PASSWORD_KW, CONNECTION_KW,
        VALID_KW, UNTIL_KW, IN_KW, OPERATOR_KW, FUNCTION_KW
    )
}
