package dev.thec1oud.plugins.ide.dpg.highlight

import dev.thec1oud.plugins.ide.dpg.DpgIcons
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.BLOCK_COMMENT_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.BLOCK_KEYWORD
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.BRACES
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.COMMA_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.DOLLAR_BODY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.DOLLAR_QUOTE_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.DOT_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.FORBIDDEN_VERB
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.IDENTIFIER_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.LINE_COMMENT_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.MACRO_KEYWORD
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.NUMBER
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.OBJECT_KEYWORD
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.OPERATOR_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.PARENS
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.SEMICOLON_KY
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.SPREAD_OPERATOR
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.SQL_KEYWORD
import dev.thec1oud.plugins.ide.dpg.highlight.DpgHighlightingColors.STRING
import com.intellij.openapi.editor.colors.TextAttributesKey
import com.intellij.openapi.fileTypes.SyntaxHighlighter
import com.intellij.openapi.options.colors.AttributesDescriptor
import com.intellij.openapi.options.colors.ColorDescriptor
import com.intellij.openapi.options.colors.ColorSettingsPage
import javax.swing.Icon

class DpgColorSettingsPage : ColorSettingsPage {

    private val ATTRIBUTES = arrayOf(
        AttributesDescriptor("Object type keyword//TABLE, VIEW, FUNCTION …", OBJECT_KEYWORD),
        AttributesDescriptor("Block directive keyword//INDEXES, GRANTS, OWNER …", BLOCK_KEYWORD),
        AttributesDescriptor("SQL keyword", SQL_KEYWORD),
        AttributesDescriptor("Macro keyword", MACRO_KEYWORD),
        AttributesDescriptor("Forbidden verb (error)//CREATE, ALTER", FORBIDDEN_VERB),
        AttributesDescriptor("Spread operator//...", SPREAD_OPERATOR),
        AttributesDescriptor("String literal//'text'", STRING),
        AttributesDescriptor("Dollar-quoted delimiter//$$", DOLLAR_QUOTE_KY),
        AttributesDescriptor("Dollar-quoted body", DOLLAR_BODY),
        AttributesDescriptor("Number", NUMBER),
        AttributesDescriptor("Line comment//-- …", LINE_COMMENT_KY),
        AttributesDescriptor("Block comment///* … */", BLOCK_COMMENT_KY),
        AttributesDescriptor("Identifier", IDENTIFIER_KY),
        AttributesDescriptor("Braces//{  }", BRACES),
        AttributesDescriptor("Parentheses//(  )", PARENS),
        AttributesDescriptor("Comma", COMMA_KY),
        AttributesDescriptor("Dot", DOT_KY),
        AttributesDescriptor("Semicolon", SEMICOLON_KY),
        AttributesDescriptor("Operator", OPERATOR_KY),
    )

    private val TAG_MAP: Map<String, TextAttributesKey> = mapOf(
        "obj"  to OBJECT_KEYWORD,
        "blk"  to BLOCK_KEYWORD,
        "sql"  to SQL_KEYWORD,
        "mac"  to MACRO_KEYWORD,
        "bad"  to FORBIDDEN_VERB,
        "spr"  to SPREAD_OPERATOR,
        "str"  to STRING,
        "dq"   to DOLLAR_QUOTE_KY,
        "dqb"  to DOLLAR_BODY,
        "num"  to NUMBER,
        "lc"   to LINE_COMMENT_KY,
        "bc"   to BLOCK_COMMENT_KY,
    )

    override fun getDisplayName(): String = "DPG"
    override fun getIcon(): Icon = DpgIcons.FILE
    override fun getHighlighter(): SyntaxHighlighter = DpgSyntaxHighlighter()
    override fun getAttributeDescriptors(): Array<AttributesDescriptor> = ATTRIBUTES
    override fun getColorDescriptors(): Array<ColorDescriptor> = ColorDescriptor.EMPTY_ARRAY
    override fun getAdditionalHighlightingTagToDescriptorMap(): Map<String, TextAttributesKey> = TAG_MAP

    override fun getDemoText(): String = """
        <lc>-- DPG source file demo</lc>
        <bc>/* Block comment */</bc>

        <mac>MACRO</mac> audit_timestamps (
            created_at TIMESTAMPTZ <sql>NOT NULL DEFAULT</sql> now(),
            deleted_at TIMESTAMPTZ
        )

        <obj>SCHEMA</obj> public {

            <obj>TYPE</obj> account_status AS ENUM ('trial', 'active', 'suspended', 'cancelled') {
                <blk>COMMENT</blk> 'Top-level account lifecycle states';
            }

            <obj>TABLE</obj> accounts (
                id     UUID  <sql>NOT NULL DEFAULT</sql> gen_random_uuid() <sql>PRIMARY KEY</sql>,
                name   TEXT  <sql>NOT NULL</sql>,
                status account_status <sql>NOT NULL DEFAULT</sql> 'trial',
                <spr>...</spr>audit_timestamps
            )
            {
                <blk>COMMENT</blk> 'Tenant account store';
                <blk>OWNER</blk> "app_admin";
                <blk>ENABLE ROW LEVEL SECURITY</blk>;

                <blk>INDEXES</blk> {
                    idx_accounts_status (status) <sql>WHERE</sql> (deleted_at <sql>IS NULL</sql>);
                }

                <blk>GRANTS</blk> {
                    <sql>SELECT</sql>, <sql>INSERT</sql> <sql>TO</sql> app_service;
                    <sql>SELECT</sql> <sql>TO</sql> app_readonly;
                }

                <blk>REVOKES</blk> {
                    <sql>ALL PRIVILEGES FROM PUBLIC</sql>;
                }
            }

            <obj>FUNCTION</obj> get_account(p_id UUID) <sql>RETURNS</sql> TEXT
            <sql>LANGUAGE</sql> sql <sql>STABLE</sql>
            <sql>AS</sql> <dq>$$</dq><dqb>
                SELECT name FROM accounts WHERE id = p_id;
            </dqb><dq>$$</dq>;
            {
                <blk>COMMENT</blk> 'Fetch an account name by ID';
            }
        }
    """.trimIndent()
}
