package dev.thec1oud.plugins.ide.dpg.lang

import dev.thec1oud.plugins.ide.dpg.lang.DpgElementTypes as E
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes
import dev.thec1oud.plugins.ide.dpg.lang.DpgTokenTypes as T
import com.intellij.lang.ASTNode
import com.intellij.lang.PsiBuilder
import com.intellij.lang.PsiParser
import com.intellij.psi.tree.IElementType

/**
 * Hand-written PsiBuilder-based parser for DPG source files.
 *
 * Grammar (simplified):
 *   dpg-file     ::= (ws | comment | macro-decl | schema-block | object-decl)*
 *   macro-decl   ::= MACRO_KW IDENTIFIER (paren-body | brace-body)
 *   schema-block ::= SCHEMA_KW qual-name LBRACE schema-body RBRACE
 *   object-decl  ::= object-kw-seq qual-name part1 [part2-block]
 *   part1        ::= tokens up to the part-1 terminator
 *   part2-block  ::= LBRACE block-content RBRACE
 */
class DpgParser : PsiParser {

    override fun parse(root: IElementType, builder: PsiBuilder): ASTNode {
        builder.setDebugMode(false)
        val rootMark = builder.mark()
        parseFile(builder)
        rootMark.done(root)
        return builder.treeBuilt
    }

    // ── File level ────────────────────────────────────────────────────────────

    private fun parseFile(b: PsiBuilder) {
        while (!b.eof()) {
            when {
                b.isTrivia()             -> b.advanceLexer()
                b.tokenType == T.MACRO_KW  -> parseMacroDeclaration(b)
                b.tokenType == T.SCHEMA_KW -> parseSchemaBlock(b)
                b.isObjectKeyword()      -> parseObjectDeclaration(b)
                else -> {
                    b.error("Unexpected token '${b.tokenText}'")
                    b.advanceLexer()
                }
            }
        }
    }

    // ── MACRO declaration ─────────────────────────────────────────────────────

    private fun parseMacroDeclaration(b: PsiBuilder) {
        val m = b.mark()
        val kwm = b.mark()
        b.advanceLexer() // MACRO_KW
        kwm.done(E.OBJECT_KEYWORD_SEQ)
        b.skipTrivia()
        if (b.tokenType == T.IDENTIFIER) {
            val nm = b.mark()
            b.advanceLexer()
            nm.done(E.QUALIFIED_NAME)
        } else b.error("Expected macro name")
        b.skipTrivia()
        when {
            b.tokenType == T.LPAREN -> { parseMacroParenBody(b); m.done(E.MACRO_DECLARATION) }
            b.tokenType == T.LBRACE -> { parseMacroBraceBody(b); m.done(E.MACRO_DECLARATION) }
            else -> {
                b.error("Expected '(' or '{' after macro name")
                m.done(E.MACRO_DECLARATION)
            }
        }
    }

    private fun parseMacroParenBody(b: PsiBuilder) {
        val m = b.mark()
        b.advanceLexer() // LPAREN
        var depth = 1
        while (!b.eof() && depth > 0) {
            when (b.tokenType) {
                T.LPAREN -> { depth++; b.advanceLexer() }
                T.RPAREN -> { depth--; if (depth > 0) b.advanceLexer() }
                else     -> b.advanceLexer()
            }
        }
        if (b.tokenType == T.RPAREN) b.advanceLexer() else b.error("Expected ')'")
        m.done(E.MACRO_PAREN_BODY)
    }

    private fun parseMacroBraceBody(b: PsiBuilder) {
        val m = b.mark()
        consumeBraceBlock(b)
        m.done(E.MACRO_BRACE_BODY)
    }

    // ── SCHEMA block ──────────────────────────────────────────────────────────

    private fun parseSchemaBlock(b: PsiBuilder) {
        val m = b.mark()
        val kwm = b.mark()
        b.advanceLexer() // SCHEMA_KW
        kwm.done(E.OBJECT_KEYWORD_SEQ)
        b.skipTrivia()
        parseQualifiedName(b)
        b.skipTrivia()
        if (b.tokenType == T.LBRACE) {
            val bm = b.mark()
            b.advanceLexer() // {
            parseSchemaBody(b)
            if (b.tokenType == T.RBRACE) b.advanceLexer() else b.error("Expected '}'")
            bm.done(E.PART2_BLOCK)
        } else {
            b.error("Expected '{' for SCHEMA body")
        }
        m.done(E.SCHEMA_BLOCK)
    }

    private fun parseSchemaBody(b: PsiBuilder) {
        while (!b.eof() && b.tokenType != T.RBRACE) {
            when {
                b.isTrivia()               -> b.advanceLexer()
                // MACRO is parsed properly so the annotator can report DPG-E007 on the keyword leaf
                b.tokenType == T.MACRO_KW  -> parseMacroDeclaration(b)
                b.tokenType == T.SCHEMA_KW -> parseSchemaBlock(b)
                b.isObjectKeyword()        -> parseObjectDeclaration(b)
                b.isPluralBlockKeyword()   -> parseNestedBlock(b)
                b.isBlockDirectiveKw()     -> parseBlockDirective(b)
                b.tokenType == T.SPREAD    -> parseSpreadExpression(b)
                else -> {
                    b.error("Unexpected '${b.tokenText}' inside SCHEMA block")
                    b.advanceLexer()
                }
            }
        }
    }

    // ── Generic object declaration ────────────────────────────────────────────

    private fun parseObjectDeclaration(b: PsiBuilder) {
        val m = b.mark()
        parseObjectKeywordSeq(b)
        b.skipTrivia()
        parseQualifiedName(b)
        b.skipTrivia()
        parsePart1(b)
        b.skipTrivia()
        if (b.tokenType == T.LBRACE) parsePart2Block(b)
        m.done(E.OBJECT_DECLARATION)
    }

    // Consume the leading DPG keyword(s) that form the object type prefix,
    // e.g. just "TABLE" or the sequence "MATERIALIZED VIEW".
    private fun parseObjectKeywordSeq(b: PsiBuilder) {
        val m = b.mark()
        val firstKw = b.tokenType
        b.advanceLexer() // first keyword already confirmed by caller
        // Multi-word sequences: MATERIALIZED VIEW, RECURSIVE VIEW, UNLOGGED TABLE,
        // FOREIGN TABLE / DATA WRAPPER, EVENT TRIGGER, TEXT SEARCH …,
        // DEFAULT PRIVILEGES, VIRTUAL TYPE, USER MAPPING, OPERATOR CLASS/FAMILY
        var justConsumedSearch = false
        while (true) {
            b.skipTrivia()
            when (b.tokenType) {
                T.VIEW_KW, T.TABLE_KW, T.TRIGGER_KW, T.TYPE_KW, T.DATA_KW -> {
                    justConsumedSearch = false
                    b.advanceLexer()
                }
                T.IDENTIFIER -> {
                    val text = b.tokenText?.uppercase() ?: break
                    when {
                        // TEXT SEARCH CONFIGURATION/DICTIONARY/PARSER/TEMPLATE:
                        // consume the third word only when we just consumed "SEARCH"
                        // after a TEXT_KW — avoids false positives on `VIEW template`
                        justConsumedSearch && text in TEXT_SEARCH_THIRD_WORDS -> {
                            justConsumedSearch = false
                            b.advanceLexer()
                        }
                        text in SECOND_WORD_IDENTIFIERS -> {
                            justConsumedSearch = firstKw == T.TEXT_KW && text == "SEARCH"
                            b.advanceLexer()
                        }
                        else -> break
                    }
                }
                // SQL_KW: "PRIVILEGES" is in SQL_KEYWORDS so it lexes as SQL_KW rather than
                // IDENTIFIER. Handle it as the second word of "DEFAULT PRIVILEGES".
                T.SQL_KW -> {
                    val text = b.tokenText?.uppercase() ?: break
                    if (firstKw == T.DEFAULT_KW && text == "PRIVILEGES") {
                        justConsumedSearch = false
                        b.advanceLexer()
                    } else break
                }
                else -> break
            }
        }
        m.done(E.OBJECT_KEYWORD_SEQ)
    }

    private fun parseQualifiedName(b: PsiBuilder) {
        if (b.tokenType != T.IDENTIFIER) return
        val m = b.mark()
        b.advanceLexer()
        while (b.tokenType == T.DOT) {
            b.advanceLexer()
            if (b.tokenType == T.IDENTIFIER) b.advanceLexer()
        }
        m.done(E.QUALIFIED_NAME)
    }

    // ── Part 1 body ───────────────────────────────────────────────────────────
    //
    // Termination rules (§4.5):
    //   T1 – ends after closing ')' for tables/composites/aggregates
    //   T2 – ends after $tag$; for functions/procedures
    //   T3 – ends at ';' for everything else
    //   Part2 { brace opens immediately after the terminator without semicolon

    private fun parsePart1(b: PsiBuilder) {
        if (b.eof() || b.tokenType == T.LBRACE) return

        val m = b.mark()
        var inDollarQuote = false
        var dqMark: PsiBuilder.Marker? = null

        loop@ while (!b.eof()) {
            when (b.tokenType) {
                T.DOLLAR_QUOTE -> {
                    if (!inDollarQuote) {
                        dqMark = b.mark()  // start DOLLAR_QUOTE_BODY before opening delimiter
                        b.advanceLexer()
                        inDollarQuote = true
                    } else {
                        b.advanceLexer()   // consume closing delimiter
                        dqMark?.done(E.DOLLAR_QUOTE_BODY)
                        dqMark = null
                        b.skipTrivia()
                        if (b.tokenType == T.SEMICOLON) b.advanceLexer()
                        break@loop
                    }
                }
                T.DOLLAR_QUOTE_CONTENT -> b.advanceLexer()

                T.SEMICOLON -> {
                    if (!inDollarQuote) {
                        b.advanceLexer()
                        break@loop
                    }
                    b.advanceLexer()
                }

                T.LBRACE -> break@loop // Part2 begins

                T.LPAREN -> parseParen(b)

                T.RPAREN -> break@loop // unmatched; stop

                else -> b.advanceLexer()
            }
        }
        dqMark?.drop() // drop if loop exited mid-dollar-quote (malformed input)
        m.done(E.PART1_BODY)
    }

    /** Consume balanced (…) including the outer parens. */
    private fun parseParen(b: PsiBuilder) {
        val m = b.mark()
        b.advanceLexer() // LPAREN
        var depth = 1
        while (!b.eof() && depth > 0) {
            when (b.tokenType) {
                T.LPAREN -> { depth++; b.advanceLexer() }
                T.RPAREN -> { depth--; b.advanceLexer() }
                T.SPREAD -> parseSpreadExpression(b)
                T.DOLLAR_QUOTE -> {
                    b.advanceLexer()
                    while (!b.eof() && b.tokenType == T.DOLLAR_QUOTE_CONTENT) b.advanceLexer()
                    if (b.tokenType == T.DOLLAR_QUOTE) b.advanceLexer()
                }
                else -> b.advanceLexer()
            }
        }
        m.done(E.PAREN_BODY)
    }

    // ── Part 2 block ──────────────────────────────────────────────────────────

    private fun parsePart2Block(b: PsiBuilder) {
        val m = b.mark()
        b.advanceLexer() // LBRACE
        parseBlockContent(b)
        if (b.tokenType == T.RBRACE) b.advanceLexer() else b.error("Expected '}'")
        m.done(E.PART2_BLOCK)
    }

    private fun parseBlockContent(b: PsiBuilder) {
        while (!b.eof() && b.tokenType != T.RBRACE) {
            when {
                b.isTrivia()              -> b.advanceLexer()
                b.isPluralBlockKeyword()  -> parseNestedBlock(b)
                b.isBlockDirectiveKw()    -> parseBlockDirective(b)
                b.tokenType == T.SPREAD   -> parseSpreadExpression(b)
                else -> {
                    b.error("Unexpected '${b.tokenText}' in block")
                    b.advanceLexer()
                }
            }
        }
    }

    private fun parseBlockDirective(b: PsiBuilder) {
        val m = b.mark()
        b.advanceLexer() // directive keyword
        while (!b.eof() && b.tokenType != T.SEMICOLON && b.tokenType != T.RBRACE) {
            when (b.tokenType) {
                T.LBRACE -> consumeBraceBlock(b)
                T.LPAREN -> parseParen(b)
                else     -> b.advanceLexer()
            }
        }
        if (b.tokenType == T.SEMICOLON) b.advanceLexer()
        m.done(E.BLOCK_DIRECTIVE)
    }

    private fun parseNestedBlock(b: PsiBuilder) {
        val m = b.mark()
        b.advanceLexer() // plural keyword
        b.skipTrivia()
        if (b.tokenType == T.LBRACE) consumeBraceBlock(b) else b.error("Expected '{' after block keyword")
        m.done(E.NESTED_BLOCK)
    }

    private fun parseSpreadExpression(b: PsiBuilder) {
        val m = b.mark()
        b.advanceLexer() // SPREAD
        b.skipTrivia()
        if (b.tokenType == T.IDENTIFIER) b.advanceLexer() else b.error("Expected macro name after '...'")
        if (b.tokenType == T.SEMICOLON) b.advanceLexer()
        m.done(E.SPREAD_EXPRESSION)
    }

    /** Consume a balanced { … } as raw tokens without building sub-nodes. */
    private fun consumeBraceBlock(b: PsiBuilder) {
        if (b.tokenType != T.LBRACE) return
        b.advanceLexer()
        var depth = 1
        while (!b.eof() && depth > 0) {
            when (b.tokenType) {
                T.LBRACE -> { depth++; b.advanceLexer() }
                T.RBRACE -> { depth--; if (depth > 0) b.advanceLexer() }
                else     -> b.advanceLexer()
            }
        }
        if (b.tokenType == T.RBRACE) b.advanceLexer() else b.error("Expected '}'")
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    private fun PsiBuilder.isTrivia() =
        tokenType == T.WHITE_SPACE || tokenType == T.LINE_COMMENT || tokenType == T.BLOCK_COMMENT

    private fun PsiBuilder.skipTrivia() { while (!eof() && isTrivia()) advanceLexer() }

    private fun PsiBuilder.isObjectKeyword(): Boolean =
        T.DPG_OBJECT_KEYWORDS.contains(tokenType)

    private fun PsiBuilder.isBlockDirectiveKw(): Boolean =
        T.BLOCK_DIRECTIVE_KEYWORDS.contains(tokenType)

    private fun PsiBuilder.isPluralBlockKeyword(): Boolean = tokenType in PLURAL_BLOCK_KEYWORDS

    companion object {
        private val PLURAL_BLOCK_KEYWORDS = setOf(
            DpgTokenTypes.INDEXES_KW,    DpgTokenTypes.POLICIES_KW,  DpgTokenTypes.TRIGGERS_KW,
            DpgTokenTypes.GRANTS_KW,     DpgTokenTypes.REVOKES_KW,
            DpgTokenTypes.PARTITIONS_KW, DpgTokenTypes.COLUMNS_KW,   DpgTokenTypes.CONSTRAINTS_KW,
            DpgTokenTypes.ATTRIBUTES_KW
        )

        private val SECOND_WORD_IDENTIFIERS = setOf(
            "SEARCH", "PRIVILEGES", "MAPPING", "WRAPPER", "CLASS", "FAMILY"
        )

        // Third word in TEXT SEARCH <X> — only consumed after "TEXT SEARCH"
        private val TEXT_SEARCH_THIRD_WORDS = setOf(
            "CONFIGURATION", "DICTIONARY", "PARSER", "TEMPLATE"
        )
    }
}
