package dev.thec1oud.plugins.ide.dpg.lang

import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.BLOCK_COMMENT
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.COLON_COLON
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.COMMA
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.DOT
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.DOLLAR_QUOTE
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.DOLLAR_QUOTE_CONTENT
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.DROP_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.FORBIDDEN_VERB
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.FUNCTION_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.GRANTS_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.IDENTIFIER
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.INDEX_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.INDEXES_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.INTEGER
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.LBRACE
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.LINE_COMMENT
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.LPAREN
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.MACRO_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.MATERIALIZED_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.MIGRATE_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.OWNER_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.RBRACE
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.RENAMED_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.RPAREN
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.ROLE_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.SCHEMA_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.SEMICOLON
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.SEQUENCE_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.SPREAD
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.SQL_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.STATISTICS_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.STRING_LITERAL
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.TABLE_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.COMMENT_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.DEPRECATED_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.PROTECTED_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.UNLOGGED_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.VIEW_KW
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes.WHITE_SPACE
import com.intellij.psi.tree.IElementType
import com.intellij.testFramework.fixtures.BasePlatformTestCase

class DpgLexerTest : BasePlatformTestCase() {

    private fun tokenize(src: String): List<Pair<IElementType, String>> {
        val lexer = DpgLexer()
        lexer.start(src)
        val result = mutableListOf<Pair<IElementType, String>>()
        while (lexer.tokenType != null) {
            result += lexer.tokenType!! to src.substring(lexer.tokenStart, lexer.tokenEnd)
            lexer.advance()
        }
        return result
    }

    private fun tokenTypes(src: String) = tokenize(src)
        .filter { it.first != WHITE_SPACE }
        .map { it.first }

    // ── Comments ──────────────────────────────────────────────────────────────

    fun testLineComment() {
        val types = tokenTypes("-- hello world\nTABLE")
        assertEquals(LINE_COMMENT, types[0])
        assertEquals(TABLE_KW, types[1])
    }

    fun testBlockComment() {
        val types = tokenTypes("/* a block */ TABLE")
        assertEquals(BLOCK_COMMENT, types[0])
        assertEquals(TABLE_KW, types[1])
    }

    // ── Keywords ──────────────────────────────────────────────────────────────

    fun testObjectKeywords() {
        val cases = mapOf(
            "TABLE" to TABLE_KW, "VIEW" to VIEW_KW, "FUNCTION" to FUNCTION_KW,
            "SCHEMA" to SCHEMA_KW, "ROLE" to ROLE_KW,
            "SEQUENCE" to SEQUENCE_KW, "MACRO" to MACRO_KW,
            "MATERIALIZED" to MATERIALIZED_KW, "UNLOGGED" to UNLOGGED_KW
        )
        cases.forEach { (word, expected) ->
            val types = tokenTypes(word)
            assertEquals("Expected $expected for $word", expected, types.single())
        }
    }

    fun testCaseInsensitiveKeywords() {
        assertEquals(listOf(TABLE_KW), tokenTypes("table"))
        assertEquals(listOf(TABLE_KW), tokenTypes("Table"))
        assertEquals(listOf(TABLE_KW), tokenTypes("TABLE"))
    }

    fun testBlockDirectiveKeywords() {
        assertEquals(INDEXES_KW,    tokenTypes("INDEXES").single())
        assertEquals(GRANTS_KW,     tokenTypes("GRANTS").single())
        assertEquals(COMMENT_KW,    tokenTypes("COMMENT").single())
        assertEquals(OWNER_KW,      tokenTypes("OWNER").single())
        assertEquals(PROTECTED_KW,  tokenTypes("PROTECTED").single())
        assertEquals(DEPRECATED_KW, tokenTypes("DEPRECATED").single())
        assertEquals(RENAMED_KW,    tokenTypes("RENAMED").single())
        assertEquals(MIGRATE_KW,    tokenTypes("MIGRATE").single())
        assertEquals(STATISTICS_KW, tokenTypes("STATISTICS").single())
        assertEquals(INDEX_KW,      tokenTypes("INDEX").single())
    }

    fun testForbiddenVerbs() {
        assertEquals(FORBIDDEN_VERB, tokenTypes("CREATE").single())
        assertEquals(FORBIDDEN_VERB, tokenTypes("ALTER").single())
        // DROP is a block directive keyword, not a forbidden verb
        assertEquals(DROP_KW, tokenTypes("DROP").single())
    }

    fun testSqlKeywords() {
        assertEquals(SQL_KW, tokenTypes("SELECT").single())
        assertEquals(SQL_KW, tokenTypes("NOT").single())
        assertEquals(SQL_KW, tokenTypes("NULL").single())
    }

    // ── Identifiers ───────────────────────────────────────────────────────────

    fun testIdentifier() {
        assertEquals(IDENTIFIER, tokenTypes("my_table").single())
        assertEquals(IDENTIFIER, tokenTypes("_underscore").single())
    }

    fun testDoubleQuotedIdentifier() {
        assertEquals(IDENTIFIER, tokenTypes(""""My Table"""").single())
    }

    // ── Literals ──────────────────────────────────────────────────────────────

    fun testSingleQuotedString() {
        assertEquals(STRING_LITERAL, tokenTypes("'hello world'").single())
    }

    fun testSingleQuotedStringWithEscape() {
        assertEquals(STRING_LITERAL, tokenTypes("'it''s fine'").single())
    }

    fun testInteger() {
        assertEquals(INTEGER, tokenTypes("42").single())
        assertEquals(INTEGER, tokenTypes("0").single())
    }

    // ── Dollar-quoted strings ─────────────────────────────────────────────────

    fun testSimpleDollarQuote() {
        val types = tokenTypes("\$\$SELECT 1\$\$")
        assertEquals(3, types.size)
        assertEquals(DOLLAR_QUOTE, types[0])
        assertEquals(DOLLAR_QUOTE_CONTENT, types[1])
        assertEquals(DOLLAR_QUOTE, types[2])
    }

    fun testTaggedDollarQuote() {
        val types = tokenTypes("\$body\$SELECT 1\$body\$")
        assertEquals(3, types.size)
        assertEquals(DOLLAR_QUOTE, types[0])
        assertEquals(DOLLAR_QUOTE_CONTENT, types[1])
        assertEquals(DOLLAR_QUOTE, types[2])
    }

    fun testEmptyDollarQuote() {
        val types = tokenTypes("\$\$\$\$")
        assertEquals(2, types.size)
        assertEquals(DOLLAR_QUOTE, types[0])
        assertEquals(DOLLAR_QUOTE, types[1])
    }

    // ── Punctuation ───────────────────────────────────────────────────────────

    fun testPunctuation() {
        assertEquals(LBRACE,    tokenTypes("{").single())
        assertEquals(RBRACE,    tokenTypes("}").single())
        assertEquals(LPAREN,    tokenTypes("(").single())
        assertEquals(RPAREN,    tokenTypes(")").single())
        assertEquals(SEMICOLON, tokenTypes(";").single())
        assertEquals(COMMA,     tokenTypes(",").single())
        assertEquals(DOT,       tokenTypes(".").single())
        assertEquals(SPREAD,    tokenTypes("...").single())
        assertEquals(COLON_COLON, tokenTypes("::").single())
    }

    // ── Full snippets ─────────────────────────────────────────────────────────

    fun testTableDeclaration() {
        val src = "TABLE users (id BIGINT NOT NULL PRIMARY KEY);"
        val types = tokenTypes(src)
        assertEquals(TABLE_KW, types[0])
        assertEquals(IDENTIFIER, types[1])   // users
        assertEquals(LPAREN, types[2])
        assertTrue(SEMICOLON in types)
    }

    fun testFunctionDeclaration() {
        val src = "FUNCTION foo() RETURNS TEXT LANGUAGE sql AS \$\$SELECT 1\$\$;"
        val types = tokenTypes(src)
        assertEquals(FUNCTION_KW, types[0])
        assertTrue(DOLLAR_QUOTE in types)
        assertTrue(DOLLAR_QUOTE_CONTENT in types)
    }
}
