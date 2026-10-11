package dev.thec1oud.plugins.ide.dpg.lang

import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes as T
import com.intellij.lexer.LexerBase
import com.intellij.psi.tree.IElementType

/**
 * Hand-written incremental lexer for the DPG language.
 *
 * Two states are used (encoded in [getState]):
 *   STATE_NORMAL       = 0  — regular scanning
 *   STATE_DOLLAR_QUOTE = 1  — inside a dollar-quoted string body
 *
 * The dollar-quote opening tag is stored in [dollarTag] and rebuilt from
 * the token stream on each [start] call.  For IntelliJ's incremental
 * re-start mid-buffer the tag must match what opened the current quote;
 * for highlighting purposes the lexer restarts at the beginning of each
 * dirty region so correctness is preserved in practice.
 */
class DpgLexer : LexerBase() {

    private var buffer: CharSequence = ""
    private var bufEnd   = 0
    private var pos      = 0
    private var tokStart = 0
    private var tokEnd   = 0
    private var tokType: IElementType? = null
    private var state    = STATE_NORMAL
    private var dollarTag = ""

    companion object {
        private const val STATE_NORMAL       = 0
        private const val STATE_DOLLAR_QUOTE = 1

        private val SQL_KEYWORDS = setOf(
            "SELECT", "FROM", "WHERE", "AS", "NOT", "NULL", "PRIMARY", "KEY",
            "REFERENCES", "ON", "DELETE", "UPDATE", "INSERT", "CASCADE", "RESTRICT",
            "DEFERRABLE", "INITIALLY", "DEFERRED", "IMMEDIATE", "NO", "ACTION",
            "CHECK", "UNIQUE", "GENERATED", "ALWAYS", "BY", "IDENTITY", "STORED",
            "RETURNS", "LANGUAGE", "SECURITY", "DEFINER", "INVOKER", "SET", "SEARCH",
            "PATH", "STABLE", "VOLATILE", "IMMUTABLE", "STRICT", "CALLED", "PARALLEL",
            "SAFE", "UNSAFE", "RESTRICTED", "ROWS", "COST", "WINDOW", "FOR", "EACH",
            "ROW", "STATEMENT", "WHEN", "EXECUTE", "BEFORE", "AFTER", "INSTEAD",
            "OF", "NEW", "OLD", "BEGIN", "END", "RETURN", "RAISE", "PERFORM",
            "DECLARE", "WITH", "WITHOUT", "OIDS", "USING", "EXCLUDE", "INCLUDING",
            "NULLS", "FIRST", "LAST", "ASC", "DESC",
            "INCLUDE", "STORAGE", "PLAIN", "EXTERNAL", "EXTENDED", "MAIN",
            "IS", "DISTINCT", "ALL", "PRIVILEGES", "TO",
            "OPTION", "REMOVE", "LEVEL", "LOGGED",
            "GLOBAL", "LOCAL", "TEMPORARY", "TEMP",
            "IF", "EXISTS", "OR", "AND", "REPLACE", "LIKE", "ILIKE",
            "BETWEEN", "ANY", "SOME", "HAVING", "GROUP", "ORDER",
            "LIMIT", "OFFSET", "UNION", "INTERSECT", "EXCEPT", "OVER",
            "FILTER", "WITHIN", "COALESCE", "NULLIF", "GREATEST", "LEAST",
            "ARRAY", "VARIADIC", "INHERITS", "ONLY",
            "NUMERIC", "INTEGER", "INT", "BIGINT", "SMALLINT", "SERIAL", "BIGSERIAL",
            "VARCHAR", "CHAR", "BOOLEAN", "BOOL", "FLOAT", "DOUBLE",
            "PRECISION", "REAL", "BYTEA", "DATE", "TIME", "TIMESTAMP", "TIMESTAMPTZ",
            "INTERVAL", "UUID", "JSON", "JSONB", "XML", "OID", "INET", "CIDR",
            "MACADDR", "BIT", "VARBIT", "MONEY", "POINT", "LINE", "LSEG",
            "BOX", "PATH", "POLYGON", "CIRCLE", "TSQUERY", "TSVECTOR",
            "RANGE", "MULTIRANGE", "HSTORE", "ENUM",
            "TRUE", "FALSE",
            "CURRENT_TIMESTAMP", "CURRENT_DATE", "CURRENT_TIME", "NOW",
            "ELSEIF", "ELSIF", "LOOP", "EXIT", "CONTINUE",
            "FOREACH", "WHILE", "QUERY", "INTO", "FOUND",
            "NOTICE", "EXCEPTION", "WARNING", "INFO", "LOG",
            "PLPGSQL", "ORDINALITY", "NOTIFY",
        )
    }

    // ── LexerBase interface ───────────────────────────────────────────────────

    // State encoding: bit 0 = DOLLAR_QUOTE flag, bits 1+ = dollarTag.length.
    // This lets IntelliJ recover the tag when the highlighter restarts mid-body.
    override fun getState(): Int =
        if (state == STATE_DOLLAR_QUOTE) (1 or (dollarTag.length shl 1)) else 0

    override fun start(buffer: CharSequence, startOffset: Int, endOffset: Int, initialState: Int) {
        this.buffer   = buffer
        this.bufEnd   = endOffset
        this.pos      = startOffset
        this.tokStart = startOffset
        this.tokEnd   = startOffset
        this.tokType  = null
        if (initialState and 1 != 0) {
            this.state    = STATE_DOLLAR_QUOTE
            val tagLen    = initialState ushr 1
            this.dollarTag = recoverTag(buffer, startOffset, tagLen)
        } else {
            this.state    = STATE_NORMAL
            this.dollarTag = ""
        }
        advance()
    }

    override fun getTokenType(): IElementType? = tokType
    override fun getTokenStart(): Int         = tokStart
    override fun getTokenEnd(): Int           = tokEnd
    override fun getBufferSequence(): CharSequence = buffer
    override fun getBufferEnd(): Int          = bufEnd

    override fun advance() {
        tokStart = tokEnd
        if (tokStart >= bufEnd) { tokType = null; return }
        pos    = tokStart
        tokType = if (state == STATE_DOLLAR_QUOTE) scanDollarBody() else scanNormal()
    }

    // ── Normal scan ───────────────────────────────────────────────────────────

    private fun scanNormal(): IElementType {
        val c = ch(pos)

        // Whitespace
        if (c.isWhitespace()) {
            while (pos < bufEnd && ch(pos).isWhitespace()) pos++
            return emit(T.WHITE_SPACE)
        }

        // Line comment  -- …
        if (c == '-' && peek(1) == '-') {
            pos += 2
            while (pos < bufEnd && ch(pos) != '\n') pos++
            if (pos < bufEnd) pos++
            return emit(T.LINE_COMMENT)
        }

        // Block comment  /* … */
        if (c == '/' && peek(1) == '*') {
            pos += 2
            while (pos < bufEnd - 1 && !(ch(pos) == '*' && ch(pos + 1) == '/')) pos++
            if (pos < bufEnd - 1) pos += 2 else pos = bufEnd
            return emit(T.BLOCK_COMMENT)
        }

        // Single-quoted string  '…'
        if (c == '\'') {
            pos++
            while (pos < bufEnd) {
                val q = ch(pos++)
                if (q == '\'' && pos < bufEnd && ch(pos) == '\'') pos++ // '' escape
                else if (q == '\'') break
            }
            return emit(T.STRING_LITERAL)
        }

        // Double-quoted identifier  "…"
        if (c == '"') {
            pos++
            while (pos < bufEnd) {
                val q = ch(pos++)
                if (q == '"' && pos < bufEnd && ch(pos) == '"') pos++ // "" escape
                else if (q == '"') break
            }
            return emit(T.IDENTIFIER)
        }

        // Dollar-quoted string  $tag$ or $$
        if (c == '$') {
            val tag = tryDollarTag(pos)
            if (tag != null) {
                dollarTag = tag
                pos += tag.length + 2  // skip $<tag>$
                state = STATE_DOLLAR_QUOTE
                return emit(T.DOLLAR_QUOTE)
            }
            pos++
            return emit(T.OPERATOR)
        }

        // Spread operator  ...
        if (c == '.' && peek(1) == '.' && peek(2) == '.') {
            pos += 3; return emit(T.SPREAD)
        }

        // Cast operator  ::
        if (c == ':' && peek(1) == ':') {
            pos += 2; return emit(T.COLON_COLON)
        }

        // Single-char punctuation
        pos++
        return when (c) {
            '{'  -> emit(T.LBRACE)
            '}'  -> emit(T.RBRACE)
            '('  -> emit(T.LPAREN)
            ')'  -> emit(T.RPAREN)
            ';'  -> emit(T.SEMICOLON)
            ','  -> emit(T.COMMA)
            '.'  -> emit(T.DOT)
            ':'  -> emit(T.COLON)
            '='  -> emit(T.EQ)
            '*'  -> emit(T.STAR)
            else -> {
                when {
                    isOpChar(c) -> {
                        while (pos < bufEnd && isOpChar(ch(pos))) pos++
                        emit(T.OPERATOR)
                    }
                    c.isDigit() -> {
                        while (pos < bufEnd && ch(pos).isDigit()) pos++
                        emit(T.INTEGER)
                    }
                    c.isLetter() || c == '_' -> {
                        while (pos < bufEnd && (ch(pos).isLetterOrDigit() || ch(pos) == '_' || ch(pos) == '$')) pos++
                        emit(classify(buffer.subSequence(tokStart, pos).toString().uppercase()))
                    }
                    else -> emit(T.BAD_CHARACTER)
                }
            }
        }
    }

    // ── Dollar-quote body scan ────────────────────────────────────────────────

    private fun scanDollarBody(): IElementType {
        val close = "\$$dollarTag\$"
        if (matchAt(pos, close)) {
            pos += close.length
            state = STATE_NORMAL
            return emit(T.DOLLAR_QUOTE)
        }
        while (pos < bufEnd && !matchAt(pos, close)) pos++
        return emit(T.DOLLAR_QUOTE_CONTENT)
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    private fun emit(type: IElementType): IElementType { tokEnd = pos; return type }
    private fun ch(i: Int): Char = if (i < bufEnd) buffer[i] else ' '
    private fun peek(off: Int): Char = ch(pos + off)

    private fun matchAt(start: Int, s: String): Boolean {
        if (start + s.length > bufEnd) return false
        for (i in s.indices) if (buffer[start + i] != s[i]) return false
        return true
    }

    /** If buffer[start] starts a valid `$tag$` delimiter, return the tag; else null. */
    private fun tryDollarTag(start: Int): String? {
        if (ch(start) != '$') return null
        var i = start + 1
        while (i < bufEnd && (ch(i).isLetterOrDigit() || ch(i) == '_')) i++
        return if (i < bufEnd && ch(i) == '$') buffer.subSequence(start + 1, i).toString() else null
    }

    /**
     * Recover the dollar-quote opening tag by scanning backward from [before].
     * Searches for `$<tag>$` where [tag] has exactly [tagLen] characters.
     * Used when the incremental highlighter restarts the lexer mid-body.
     */
    private fun recoverTag(buf: CharSequence, before: Int, tagLen: Int): String {
        var i = before - 1
        while (i >= tagLen + 1) {
            if (buf[i] == '$' && buf[i - tagLen - 1] == '$') {
                val tag = buf.substring(i - tagLen, i)
                if (tag.all { c -> c.isLetterOrDigit() || c == '_' }) return tag
            }
            i--
        }
        return ""
    }

    private fun isOpChar(c: Char) = c in "+-<>!@#%^&|~`\\"

    // ── Keyword classifier (case-insensitive; input must already be uppercased) ──

    private fun classify(w: String): IElementType = when (w) {
        // Object-type keywords
        "SCHEMA"         -> T.SCHEMA_KW
        "TABLE"          -> T.TABLE_KW
        "VIEW"           -> T.VIEW_KW
        "FUNCTION"       -> T.FUNCTION_KW
        "PROCEDURE"      -> T.PROCEDURE_KW
        "AGGREGATE"      -> T.AGGREGATE_KW
        "TYPE"           -> T.TYPE_KW
        "DOMAIN"         -> T.DOMAIN_KW
        "SEQUENCE"       -> T.SEQUENCE_KW
        "ROLE"           -> T.ROLE_KW
        "TABLESPACE"     -> T.TABLESPACE_KW
        "EXTENSION"      -> T.EXTENSION_KW
        "MACRO"          -> T.MACRO_KW
        "PUBLICATION"    -> T.PUBLICATION_KW
        "SUBSCRIPTION"   -> T.SUBSCRIPTION_KW
        "COLLATION"      -> T.COLLATION_KW
        "OPERATOR"       -> T.OPERATOR_KW
        "CAST"           -> T.CAST_KW
        "SERVER"         -> T.SERVER_KW
        // Multi-word leading keywords
        "MATERIALIZED"   -> T.MATERIALIZED_KW
        "RECURSIVE"      -> T.RECURSIVE_KW
        "UNLOGGED"       -> T.UNLOGGED_KW
        "FOREIGN"        -> T.FOREIGN_KW
        "EVENT"          -> T.EVENT_KW
        "TEXT"           -> T.TEXT_KW
        "DEFAULT"        -> T.DEFAULT_KW
        "VIRTUAL"        -> T.VIRTUAL_KW
        "USER"           -> T.USER_KW
        "DATA"           -> T.DATA_KW
        // Block directive keywords
        "INDEX"          -> T.INDEX_KW
        "INDEXES"        -> T.INDEXES_KW
        "POLICY"         -> T.POLICY_KW
        "POLICIES"       -> T.POLICIES_KW
        "TRIGGER"        -> T.TRIGGER_KW
        "TRIGGERS"       -> T.TRIGGERS_KW
        "GRANT"          -> T.GRANT_KW
        "GRANTS"         -> T.GRANTS_KW
        "REVOKE"     -> T.REVOKE_KW
        "REVOKES"    -> T.REVOKES_KW
        "PARTITION"      -> T.PARTITION_KW
        "PARTITIONS"     -> T.PARTITIONS_KW
        "COLUMN"         -> T.COLUMN_KW
        "COLUMNS"        -> T.COLUMNS_KW
        "ATTRIBUTE"      -> T.ATTRIBUTE_KW
        "ATTRIBUTES"     -> T.ATTRIBUTES_KW
        "CONSTRAINT"     -> T.CONSTRAINT_KW
        "CONSTRAINTS"    -> T.CONSTRAINTS_KW
        "COMMENT"        -> T.COMMENT_KW
        "OWNER"          -> T.OWNER_KW
        "PROTECTED"      -> T.PROTECTED_KW
        "DEPRECATED"     -> T.DEPRECATED_KW
        "RENAMED"        -> T.RENAMED_KW
        "MIGRATE"        -> T.MIGRATE_KW
        "ENABLE"         -> T.ENABLE_KW
        "DISABLE"        -> T.DISABLE_KW
        "FORCE"          -> T.FORCE_KW
        "NOFORCE"        -> T.NOFORCE_KW
        "DROP"           -> T.DROP_KW
        "STATISTICS"     -> T.STATISTICS_KW
        // Role attribute keywords
        "LOGIN"          -> T.LOGIN_KW
        "NOLOGIN"        -> T.NOLOGIN_KW
        "SUPERUSER"      -> T.SUPERUSER_KW
        "NOSUPERUSER"    -> T.NOSUPERUSER_KW
        "CREATEDB"       -> T.CREATEDB_KW
        "NOCREATEDB"     -> T.NOCREATEDB_KW
        "CREATEROLE"     -> T.CREATEROLE_KW
        "NOCREATEROLE"   -> T.NOCREATEROLE_KW
        "INHERIT"        -> T.INHERIT_KW
        "NOINHERIT"      -> T.NOINHERIT_KW
        "REPLICATION"    -> T.REPLICATION_KW
        "NOREPLICATION"  -> T.NOREPLICATION_KW
        "BYPASSRLS"      -> T.BYPASSRLS_KW
        "NOBYPASSRLS"    -> T.NOBYPASSRLS_KW
        "PASSWORD"       -> T.PASSWORD_KW
        "CONNECTION"     -> T.CONNECTION_KW
        "VALID"          -> T.VALID_KW
        "UNTIL"          -> T.UNTIL_KW
        "IN"             -> T.IN_KW
        // Forbidden verbs (CREATE and ALTER are never legal at declaration level)
        "CREATE", "ALTER" -> T.FORBIDDEN_VERB
        // DROP is a block directive keyword, not a forbidden verb
        // SQL keywords and everything else
        else -> if (SQL_KEYWORDS.contains(w)) T.SQL_KW else T.IDENTIFIER
    }
}
