package dev.thec1oud.plugins.ide.dpg.lang

import dev.thec1oud.plugins.ide.dpg.lang.DpgElementTypes.MACRO_DECLARATION
import dev.thec1oud.plugins.ide.dpg.lang.DpgElementTypes.OBJECT_KEYWORD_SEQ
import dev.thec1oud.plugins.ide.dpg.lang.DpgElementTypes.PART2_BLOCK
import dev.thec1oud.plugins.ide.dpg.lang.DpgElementTypes.SCHEMA_BLOCK
import dev.thec1oud.plugins.ide.dpg.lang.DpgElementTypes.SPREAD_EXPRESSION
import dev.thec1oud.plugins.ide.dpg.lang.psi.DpgFile
import dev.thec1oud.plugins.ide.dpg.lang.psi.DpgObjectDeclaration
import dev.thec1oud.plugins.ide.dpg.lang.psi.DpgPsiElement
import com.intellij.psi.util.PsiTreeUtil
import com.intellij.testFramework.ParsingTestCase

class DpgParserTest : ParsingTestCase("", "dpg", DpgParserDefinition()) {

    override fun getTestDataPath(): String = "src/test/testData"

    // ── Root node type ────────────────────────────────────────────────────────

    fun testEmptyFile() {
        assertInstanceOf(parseFile("empty", ""), DpgFile::class.java)
    }

    fun testCommentsOnly() {
        assertNotNull(parseFile("comments", "-- line comment\n/* block comment */"))
    }

    // ── Table ─────────────────────────────────────────────────────────────────

    fun testSimpleTable() {
        val file = parseFile("table", "TABLE users (id BIGINT NOT NULL PRIMARY KEY);")
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals(1, decls!!.size)
        assertEquals("users", decls[0].name)
    }

    fun testTableWithPart2Block() {
        val file = parseFile("table_block", """
            TABLE accounts (
                id UUID NOT NULL PRIMARY KEY
            )
            {
                COMMENT 'Tenant store';
                ENABLE ROW LEVEL SECURITY;
            }
        """.trimIndent())
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals(1, decls!!.size)
        assertEquals("accounts", decls[0].name)
        assertNotNull(decls[0].node.findChildByType(PART2_BLOCK))
    }

    // ── Schema block ──────────────────────────────────────────────────────────

    fun testSchemaBlock() {
        val file = parseFile("schema", """
            SCHEMA public {
                TABLE users (id BIGINT);
            }
        """.trimIndent())
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        val schema = decls!!.firstOrNull { it.node.elementType == SCHEMA_BLOCK }
        assertNotNull(schema)
        assertEquals("public", schema!!.name)
    }

    // ── Macro declaration ─────────────────────────────────────────────────────

    fun testMacroDeclaration() {
        val file = parseFile("macro", """
            MACRO timestamps (
                created_at TIMESTAMPTZ NOT NULL DEFAULT now()
            )
        """.trimIndent())
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals(1, decls!!.size)
        assertEquals(MACRO_DECLARATION, decls[0].node.elementType)
    }

    // ── View ──────────────────────────────────────────────────────────────────

    fun testView() {
        val file = parseFile("view", "VIEW active_users AS SELECT id FROM users WHERE active;")
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals("active_users", decls!![0].name)
    }

    // ── Function ──────────────────────────────────────────────────────────────

    fun testFunction() {
        val src = """
            FUNCTION greet(name TEXT) RETURNS TEXT LANGUAGE sql STABLE
            AS ${'$'}${'$'}
                SELECT 'Hello, ' || name;
            ${'$'}${'$'};
            {
                COMMENT 'Greet a user';
            }
        """.trimIndent()
        val file = parseFile("function", src)
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals(1, decls!!.size)
        assertEquals("greet", decls[0].name)
        assertNotNull(decls[0].node.findChildByType(PART2_BLOCK))
    }

    // ── Enum ──────────────────────────────────────────────────────────────────

    fun testEnum() {
        val file = parseFile("enum", "TYPE status AS ENUM ('active', 'inactive', 'deleted');")
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals("status", decls!![0].name)
    }

    // ── Role ──────────────────────────────────────────────────────────────────

    fun testRole() {
        val file = parseFile("role", """
            ROLE app_service {
                LOGIN;
                PASSWORD 'env:APP_PW';
                CONNECTION LIMIT 20;
            }
        """.trimIndent())
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals("app_service", decls!![0].name)
    }

    // ── Multiple declarations ─────────────────────────────────────────────────

    fun testMultipleDeclarations() {
        val file = parseFile("multiple", """
            TABLE a (id BIGINT);
            TABLE b (id BIGINT);
            VIEW v AS SELECT 1;
        """.trimIndent())
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals(3, decls!!.size)
    }

    // ── Spread expression ─────────────────────────────────────────────────────

    fun testSpreadExpression() {
        val src = """
            MACRO timestamps (created_at TIMESTAMPTZ)
            TABLE t (id BIGINT, ...timestamps);
        """.trimIndent()
        val file = parseFile("spread", src)
        assertNotNull(file)
        val spreadExprs = PsiTreeUtil.findChildrenOfType(file, DpgPsiElement::class.java)
            .filter { it.node.elementType == SPREAD_EXPRESSION }
        assertEquals("Expected one SPREAD_EXPRESSION node inside paren body", 1, spreadExprs.size)
    }

    fun testSpreadExpressionInSchemaBlock() {
        val src = """
            MACRO ts (x INT)
            SCHEMA public {
                ...ts;
            }
        """.trimIndent()
        val file = parseFile("spread_schema", src)
        assertNotNull(file)
        val spreadExprs = PsiTreeUtil.findChildrenOfType(file, DpgPsiElement::class.java)
            .filter { it.node.elementType == SPREAD_EXPRESSION }
        assertEquals("Expected one SPREAD_EXPRESSION node inside schema body", 1, spreadExprs.size)
    }

    fun testMacroDeclarationHasObjectKeywordSeq() {
        val file = parseFile("macro_kw_seq", "MACRO ts (x INT)")
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        val macro = decls!!.first { it.node.elementType == MACRO_DECLARATION }
        assertNotNull("MACRO_DECLARATION should have an OBJECT_KEYWORD_SEQ child",
            macro.node.findChildByType(OBJECT_KEYWORD_SEQ))
        assertEquals("MACRO", macro.getObjectKindText())
    }

    // ── Extension ─────────────────────────────────────────────────────────────

    fun testExtension() {
        val file = parseFile("extension", "EXTENSION pgcrypto;")
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        assertEquals("pgcrypto", decls!![0].name)
    }

    // ── Operator family loose members (RFC §14.4) ────────────────────────────

    fun testOperatorFamilyWithLooseMembers() {
        // No trailing ';' after the family's own '}': the DPG scanner treats
        // that as optional for every object kind (readOptionalPart2 in
        // internal/scanner/scanner.go), but the IntelliJ plugin's parser has
        // a separate, pre-existing gap there unrelated to this feature — see
        // parseObjectDeclaration, which never consumes one. Out of scope
        // here; this test only exercises the new OPERATOR/FUNCTION member
        // directives themselves.
        val file = parseFile("opfamily", """
            SCHEMA public {
                OPERATOR FAMILY my_family USING btree {
                    OPERATOR 1 <(int4, int8),
                    OPERATOR 3 =(int4, int8) FOR ORDER BY my_family,
                    FUNCTION 1 (int4, int8) btint48cmp(int4, int8)
                }
            }
        """.trimIndent())
        val err = PsiTreeUtil.findChildOfType(file, com.intellij.psi.PsiErrorElement::class.java)
        assertNull("expected no parse errors, found: ${err?.errorDescription}", err)
        val decls = PsiTreeUtil.getChildrenOfType(file, DpgObjectDeclaration::class.java)
        assertNotNull(decls)
        val schema = decls!!.firstOrNull { it.node.elementType == SCHEMA_BLOCK }
        assertNotNull(schema)
        // The family declaration is nested inside the schema's own
        // PART2_BLOCK, not a direct child of the schema's
        // DpgObjectDeclaration — findChildOfType searches descendants.
        val fam = PsiTreeUtil.findChildOfType(schema, DpgObjectDeclaration::class.java)
        assertNotNull(fam)
        assertEquals("my_family", fam!!.name)
        assertNotNull(fam.node.findChildByType(PART2_BLOCK))
    }
}
