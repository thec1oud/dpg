/// <reference types="tree-sitter-cli/dsl" />
// @ts-check

module.exports = grammar({
  name: "dpg",

  conflicts: $ => [
    // `ROLE name ROLE other_role`: second ROLE could be a new declaration
    // or the _role_option `ROLE role_list`. GLR resolves at parse time.
    [$.role_declaration],
    // `name type CONSTRAINT ...`: after a bare type, a constraint keyword could
    // end the repeat or start a new _column_constraint. GLR resolves correctly.
    [$.column_def],
  ],

  externals: $ => [
    $.dollar_quoted_string,
    $._block_comment_content,
  ],

  extras: $ => [
    /\s+/,
    $.line_comment,
    $.block_comment,
  ],

  word: $ => $.identifier,

  supertypes: $ => [
    $._declaration,
    $._block_directive,
  ],

  rules: {
    source_file: $ => repeat($._declaration),

    // ─── Top-level declarations ───────────────────────────────────────────────

    _declaration: $ => choice(
      $.macro_declaration,
      $.schema_block,
      $.table_declaration,
      $.view_declaration,
      $.materialized_view_declaration,
      $.recursive_view_declaration,
      $.function_declaration,
      $.procedure_declaration,
      $.aggregate_declaration,
      $.enum_declaration,
      $._type_declaration,
      $.domain_declaration,
      $.virtual_type_declaration,
      $.sequence_declaration,
      $.role_declaration,
      $.tablespace_declaration,
      $.schema_declaration,
      $.extension_declaration,
      $.publication_declaration,
      $.subscription_declaration,
      $.event_trigger_declaration,
      $.default_privileges_declaration,
      $.fdw_declaration,
      $.foreign_server_declaration,
      $.user_mapping_declaration,
      $.text_search_config_declaration,
      $.text_search_dict_declaration,
      $.text_search_parser_declaration,
      $.text_search_template_declaration,
      $.collation_declaration,
      $.operator_declaration,
      $.operator_class_declaration,
      $.operator_family_declaration,
      $.cast_declaration,
      $.statistics_declaration,
    ),

    // ─── MACRO ────────────────────────────────────────────────────────────────

    macro_declaration: $ => seq(
      kw("MACRO"),
      field("name", $.identifier),
      choice(
        seq("(", field("body", $._paren_body), ")"),
        seq("{", field("body", $._brace_body), "}"),
      ),
    ),

    macro_spread: $ => seq(
      "...",
      field("name", $.identifier),
    ),

    // ─── SCHEMA block (scopes nested objects) ─────────────────────────────────

    schema_block: $ => seq(
      kw("SCHEMA"),
      field("name", $.identifier),
      "{",
      repeat(choice(
        $._declaration,
        $.owner_directive,
        $.comment_directive,
        $.renamed_from_directive,
      )),
      "}",
    ),

    // ─── TABLE ────────────────────────────────────────────────────────────────

    table_declaration: $ => seq(
      optional(choice(kw("UNLOGGED"), kw("FOREIGN"))),
      kw("TABLE"),
      field("name", $._object_ref),
      field("columns", $.column_list),
      optional($.dpg_block),
      optional(";"),
    ),

    column_list: $ => seq(
      "(",
      commaSep(choice(
        $.column_def,
        $.table_constraint,
        $.macro_spread,
      )),
      optional(","),
      ")",
    ),

    column_def: $ => seq(
      field("name", $.identifier),
      field("type", $.sql_type),
      repeat($._column_constraint),
    ),

    _column_constraint: $ => choice(
      seq(kw("NOT"), kw("NULL")),
      seq(kw("NULL")),
      seq(kw("UNIQUE")),
      seq(kw("PRIMARY"), kw("KEY")),
      seq(kw("DEFAULT"), $.sql_expression),
      seq(kw("REFERENCES"), $._object_ref, optional(seq("(", $.identifier, ")"))),
      seq(kw("GENERATED"), kw("ALWAYS"), kw("AS"), kw("IDENTITY")),
      seq(kw("GENERATED"), kw("BY"), kw("DEFAULT"), kw("AS"), kw("IDENTITY")),
      seq(kw("CHECK"), "(", $.sql_expression, ")"),
      seq(kw("CONSTRAINT"), $.identifier, $._column_constraint),
    ),

    table_constraint: $ => seq(
      optional(seq(kw("CONSTRAINT"), field("name", $.identifier))),
      choice(
        seq(kw("PRIMARY"), kw("KEY"), "(", commaSep1($.identifier), ")"),
        seq(kw("UNIQUE"), "(", commaSep1($.identifier), ")"),
        seq(kw("FOREIGN"), kw("KEY"), "(", commaSep1($.identifier), ")",
            kw("REFERENCES"), $._object_ref,
            optional(seq("(", commaSep1($.identifier), ")"))),
        seq(kw("CHECK"), "(", $.sql_expression, ")", optional(seq(kw("NOT"), kw("VALID")))),
        seq(kw("EXCLUDE"), $.sql_expression),
      ),
    ),

    // ─── VIEW ─────────────────────────────────────────────────────────────────

    view_declaration: $ => seq(
      kw("VIEW"),
      field("name", $._object_ref),
      optional(seq("(", commaSep1($.identifier), ")")),
      kw("AS"),
      field("query", $.sql_expression),
      optional($.dpg_block),
      optional(";"),
    ),

    materialized_view_declaration: $ => seq(
      kw("MATERIALIZED"), kw("VIEW"),
      field("name", $._object_ref),
      kw("AS"),
      field("query", $.sql_expression),
      optional(seq(kw("WITH"), optional(kw("NO")), kw("DATA"))),
      optional($.dpg_block),
      optional(";"),
    ),

    recursive_view_declaration: $ => seq(
      kw("RECURSIVE"), kw("VIEW"),
      field("name", $._object_ref),
      "(",
      commaSep1($.identifier),
      ")",
      kw("AS"),
      field("query", $.sql_expression),
      optional($.dpg_block),
      optional(";"),
    ),

    // ─── FUNCTION / PROCEDURE / AGGREGATE ────────────────────────────────────

    function_declaration: $ => seq(
      kw("FUNCTION"),
      field("name", $._object_ref),
      field("params", $.param_list),
      kw("RETURNS"),
      field("returns", $.sql_type),
      repeat($._function_option),
      kw("AS"),
      field("body", choice($.dollar_quoted_string, $.string_literal)),
      optional($.dpg_block),
      optional(";"),
    ),

    procedure_declaration: $ => seq(
      kw("PROCEDURE"),
      field("name", $._object_ref),
      field("params", $.param_list),
      kw("LANGUAGE"),
      field("language", $.identifier),
      kw("AS"),
      field("body", choice($.dollar_quoted_string, $.string_literal)),
      optional($.dpg_block),
      optional(";"),
    ),

    aggregate_declaration: $ => seq(
      kw("AGGREGATE"),
      field("name", $._object_ref),
      "(",
      commaSep1($.sql_type),
      ")",
      "(",
      commaSep1($.aggregate_option),
      ")",
      optional($.dpg_block),
      optional(";"),
    ),

    aggregate_option: $ => seq(
      $.identifier, "=", choice($.identifier, $.string_literal),
    ),

    _function_option: $ => choice(
      seq(kw("LANGUAGE"), field("language", $.identifier)),
      kw("IMMUTABLE"), kw("STABLE"), kw("VOLATILE"),
      kw("STRICT"),
      seq(kw("SECURITY"), kw("DEFINER")),
      seq(kw("SECURITY"), kw("INVOKER")),
      seq(kw("PARALLEL"), choice(kw("UNSAFE"), kw("RESTRICTED"), kw("SAFE"))),
      seq(kw("COST"), $.number_literal),
      seq(kw("ROWS"), $.number_literal),
      seq(kw("SET"), $.identifier, "=", $.sql_expression),
    ),

    param_list: $ => seq("(", optional(commaSep($.param_def)), ")"),

    param_def: $ => seq(
      optional(choice(kw("IN"), kw("OUT"), kw("INOUT"), kw("VARIADIC"))),
      optional(field("name", $.identifier)),
      field("type", $.sql_type),
      optional(seq("=", $.sql_expression)),
    ),

    // ─── ENUM ─────────────────────────────────────────────────────────────────

    enum_declaration: $ => seq(
      kw("TYPE"),
      field("name", $._object_ref),
      kw("AS"), kw("ENUM"),
      "(",
      commaSep1($.string_literal),
      ")",
      optional($.dpg_block),
      optional(";"),
    ),

    // ─── TYPE (composite / range / base / virtual) ────────────────────────────

    _type_declaration: $ => choice(
      $.composite_type_declaration,
      $.range_type_declaration,
      $.base_type_declaration,
    ),

    composite_type_declaration: $ => seq(
      kw("TYPE"),
      field("name", $._object_ref),
      kw("AS"),
      "(",
      commaSep(seq($.identifier, $.sql_type)),
      ")",
      optional($.dpg_block),
      optional(";"),
    ),

    range_type_declaration: $ => seq(
      kw("TYPE"),
      field("name", $._object_ref),
      kw("AS"), kw("RANGE"),
      "(",
      commaSep1(seq($.identifier, "=", choice($.identifier, $.string_literal))),
      ")",
      optional($.dpg_block),
      optional(";"),
    ),

    base_type_declaration: $ => seq(
      kw("TYPE"),
      field("name", $._object_ref),
      "(",
      commaSep1(seq($.identifier, "=", $.sql_expression)),
      ")",
      optional($.dpg_block),
      optional(";"),
    ),

    domain_declaration: $ => seq(
      kw("DOMAIN"),
      field("name", $._object_ref),
      kw("AS"),
      field("base", $.sql_type),
      optional($.dpg_block),
      optional(";"),
    ),

    virtual_type_declaration: $ => seq(
      kw("VIRTUAL"), kw("TYPE"),
      field("name", $._object_ref),
      kw("AS"),
      field("expression", $.sql_type),
      optional($.dpg_block),
      optional(";"),
    ),

    // ─── SEQUENCE ─────────────────────────────────────────────────────────────

    sequence_declaration: $ => seq(
      kw("SEQUENCE"),
      field("name", $._object_ref),
      repeat($._sequence_option),
      optional($.dpg_block),
      optional(";"),
    ),

    _sequence_option: $ => choice(
      seq(kw("AS"), $.sql_type),
      seq(kw("INCREMENT"), optional(kw("BY")), $.number_literal),
      seq(kw("MINVALUE"), $.number_literal),
      seq(kw("NO"), kw("MINVALUE")),
      seq(kw("MAXVALUE"), $.number_literal),
      seq(kw("NO"), kw("MAXVALUE")),
      seq(kw("START"), optional(kw("WITH")), $.number_literal),
      seq(kw("CACHE"), $.number_literal),
      kw("CYCLE"),
      seq(kw("NO"), kw("CYCLE")),
      seq(kw("OWNED"), kw("BY"), $._object_ref),
    ),

    // ─── ROLE ─────────────────────────────────────────────────────────────────

    role_declaration: $ => seq(
      kw("ROLE"),
      field("name", $.identifier),
      repeat($._role_option),
      optional($.dpg_block),
      optional(";"),
    ),

    _role_option: $ => choice(
      kw("LOGIN"), kw("NOLOGIN"),
      kw("SUPERUSER"), kw("NOSUPERUSER"),
      kw("CREATEDB"), kw("NOCREATEDB"),
      kw("CREATEROLE"), kw("NOCREATEROLE"),
      kw("INHERIT"), kw("NOINHERIT"),
      kw("REPLICATION"), kw("NOREPLICATION"),
      kw("BYPASSRLS"), kw("NOBYPASSRLS"),
      seq(kw("CONNECTION"), kw("LIMIT"), $.number_literal),
      seq(kw("PASSWORD"), $.string_literal),
      seq(kw("VALID"), kw("UNTIL"), $.string_literal),
      seq(kw("IN"), kw("ROLE"), commaSep1($.identifier)),
      seq(kw("ROLE"), commaSep1($.identifier)),
      seq(kw("ADMIN"), commaSep1($.identifier)),
    ),

    // ─── TABLESPACE ───────────────────────────────────────────────────────────

    tablespace_declaration: $ => seq(
      kw("TABLESPACE"),
      field("name", $.identifier),
      kw("LOCATION"),
      field("location", $.string_literal),
      optional($.dpg_block),
      optional(";"),
    ),

    // ─── SCHEMA (standalone, no block) ────────────────────────────────────────

    schema_declaration: $ => seq(
      kw("SCHEMA"),
      field("name", $.identifier),
      ";",
    ),

    // ─── EXTENSION ────────────────────────────────────────────────────────────

    extension_declaration: $ => seq(
      kw("EXTENSION"),
      field("name", $.identifier),
      optional(seq(kw("SCHEMA"), $.identifier)),
      optional(seq(kw("VERSION"), $.string_literal)),
      optional(kw("CASCADE")),
      ";",
    ),

    // ─── PUBLICATION / SUBSCRIPTION ───────────────────────────────────────────

    publication_declaration: $ => seq(
      kw("PUBLICATION"),
      field("name", $.identifier),
      choice(
        seq(kw("FOR"), kw("ALL"), kw("TABLES")),
        seq(kw("FOR"), kw("TABLE"), commaSep1($._object_ref)),
        seq(kw("FOR"), kw("ALL"), kw("TABLES"), kw("IN"), kw("SCHEMA"), commaSep1($.identifier)),
      ),
      optional(seq(kw("WITH"), "(", commaSep1($.publication_option), ")")),
      optional($.dpg_block),
      optional(";"),
    ),

    publication_option: $ => seq($.identifier, "=", $.string_literal),

    subscription_declaration: $ => seq(
      kw("SUBSCRIPTION"),
      field("name", $.identifier),
      kw("CONNECTION"), field("connstr", $.string_literal),
      kw("PUBLICATION"), commaSep1($.identifier),
      optional(seq(kw("WITH"), "(", commaSep1($.subscription_option), ")")),
      optional(";"),
    ),

    subscription_option: $ => seq($.identifier, "=", choice($.string_literal, $.boolean_literal, $.identifier)),

    // ─── EVENT TRIGGER ────────────────────────────────────────────────────────

    event_trigger_declaration: $ => seq(
      kw("EVENT"), kw("TRIGGER"),
      field("name", $.identifier),
      kw("ON"), field("event", $.identifier),
      optional(seq(kw("WHEN"), kw("TAG"), kw("IN"), "(", commaSep1($.string_literal), ")")),
      kw("EXECUTE"), kw("FUNCTION"),
      field("function", $._object_ref), "(", ")",
      optional(";"),
    ),

    // ─── DEFAULT PRIVILEGES ───────────────────────────────────────────────────

    default_privileges_declaration: $ => seq(
      kw("DEFAULT"), kw("PRIVILEGES"),
      optional(seq(kw("FOR"), kw("ROLE"), $.identifier)),
      optional(seq(kw("IN"), kw("SCHEMA"), $.identifier)),
      $.dpg_block,
    ),

    // ─── FDW / SERVER / USER MAPPING ─────────────────────────────────────────

    fdw_declaration: $ => seq(
      kw("FOREIGN"), kw("DATA"), kw("WRAPPER"),
      field("name", $.identifier),
      optional(seq(kw("HANDLER"), $.identifier)),
      optional(seq(kw("VALIDATOR"), $.identifier)),
      optional($.options_clause),
      optional(";"),
    ),

    foreign_server_declaration: $ => seq(
      kw("SERVER"),
      field("name", $.identifier),
      optional(seq(kw("TYPE"), $.string_literal)),
      optional(seq(kw("VERSION"), $.string_literal)),
      kw("FOREIGN"), kw("DATA"), kw("WRAPPER"), $.identifier,
      optional($.options_clause),
      optional(";"),
    ),

    user_mapping_declaration: $ => seq(
      kw("USER"), kw("MAPPING"),
      kw("FOR"), field("user", choice(kw("PUBLIC"), $.identifier)),
      kw("SERVER"), $.identifier,
      optional($.options_clause),
      optional(";"),
    ),

    options_clause: $ => seq(
      kw("OPTIONS"), "(", commaSep1($.option_pair), ")",
    ),

    option_pair: $ => seq($.identifier, $.string_literal),

    // ─── TEXT SEARCH objects ──────────────────────────────────────────────────

    text_search_config_declaration: $ => seq(
      kw("TEXT"), kw("SEARCH"), kw("CONFIGURATION"),
      field("name", $._object_ref),
      "(", commaSep1($.option_pair), ")",
      optional($.dpg_block),
      optional(";"),
    ),

    text_search_dict_declaration: $ => seq(
      kw("TEXT"), kw("SEARCH"), kw("DICTIONARY"),
      field("name", $._object_ref),
      "(", commaSep1($.option_pair), ")",
      optional($.dpg_block),
      optional(";"),
    ),

    text_search_parser_declaration: $ => seq(
      kw("TEXT"), kw("SEARCH"), kw("PARSER"),
      field("name", $._object_ref),
      "(", commaSep1($.option_pair), ")",
      optional($.dpg_block),
      optional(";"),
    ),

    text_search_template_declaration: $ => seq(
      kw("TEXT"), kw("SEARCH"), kw("TEMPLATE"),
      field("name", $._object_ref),
      "(", commaSep1($.option_pair), ")",
      optional($.dpg_block),
      optional(";"),
    ),

    // ─── COLLATION / OPERATOR / CAST / STATISTICS ────────────────────────────

    collation_declaration: $ => seq(
      kw("COLLATION"),
      field("name", $._object_ref),
      "(", commaSep1($.option_pair), ")",
      optional(";"),
    ),

    operator_declaration: $ => seq(
      kw("OPERATOR"),
      field("name", $._object_ref),
      "(", commaSep1($.option_pair), ")",
      optional(";"),
    ),

    operator_class_declaration: $ => seq(
      kw("OPERATOR"), kw("CLASS"),
      field("name", $._object_ref),
      optional(kw("DEFAULT")),
      kw("FOR"), kw("TYPE"), $.sql_type,
      kw("USING"), $.identifier,
      optional(seq(kw("FAMILY"), $._object_ref)),
      kw("AS"), "(", commaSep1($.sql_expression), ")",
      optional(";"),
    ),

    operator_family_declaration: $ => seq(
      kw("OPERATOR"), kw("FAMILY"),
      field("name", $._object_ref),
      kw("USING"), $.identifier,
      optional($.dpg_block),
      optional(";"),
    ),

    cast_declaration: $ => seq(
      kw("CAST"),
      "(", $.sql_type, kw("AS"), $.sql_type, ")",
      choice(
        seq(kw("WITH"), kw("FUNCTION"), $._object_ref, optional(seq("(", commaSep($.sql_type), ")"))),
        seq(kw("WITHOUT"), kw("FUNCTION")),
        seq(kw("WITH"), kw("INOUT")),
      ),
      optional(choice(
        seq(kw("AS"), kw("IMPLICIT")),
        seq(kw("AS"), kw("ASSIGNMENT")),
      )),
      optional(";"),
    ),

    statistics_declaration: $ => seq(
      kw("STATISTICS"),
      field("name", $._object_ref),
      optional(seq("(", commaSep1($.identifier), ")")),
      kw("ON"), commaSep1($.identifier),
      kw("FROM"), $._object_ref,
      optional(";"),
    ),

    // ─── DPG block ({ }) ──────────────────────────────────────────────────────

    dpg_block: $ => seq(
      "{",
      repeat(choice($._block_directive, $.macro_spread)),
      "}",
    ),

    _block_directive: $ => choice(
      $.comment_directive,
      $.owner_directive,
      $.renamed_from_directive,
      $.protected_directive,
      $.deprecated_directive,
      $.drop_cascade_directive,
      $.indexes_block,
      $.policies_block,
      $.triggers_block,
      $.columns_block,
      $.constraints_block,
      $.grants_block,
      $.revokes_block,
      $.partitions_block,
      $.migrate_remove_directive,
      $.enum_value_rename_directive,
      $.attributes_block,
      $.attribute_block,
      $.grant_directive,
      $.revoke_directive,
      $.default_directive,
      $.not_null_directive,
      $.check_directive,
      $.mapping_directive,
      $.opfamily_operator_member,
      $.opfamily_function_member,
      $.name_map_directive,
      $.name_maps_block,
    ),

    comment_directive: $ => seq(
      kw("COMMENT"), field("text", $.string_literal), ";",
    ),

    owner_directive: $ => seq(
      kw("OWNER"), field("role", $.identifier), ";",
    ),

    renamed_from_directive: $ => seq(
      kw("RENAMED"), kw("FROM"), field("old_name", $.identifier), ";",
    ),

    // Enum value rename: VALUE 'new' RENAMED FROM 'old';
    enum_value_rename_directive: $ => seq(
      kw("VALUE"), field("name", $.string_literal),
      kw("RENAMED"), kw("FROM"), field("old_name", $.string_literal), ";",
    ),

    protected_directive: $ => seq(kw("PROTECTED"), ";"),

    deprecated_directive: $ => seq(
      kw("DEPRECATED"), field("message", optional($.string_literal)), ";",
    ),

    drop_cascade_directive: $ => seq(kw("DROP"), kw("CASCADE"), ";"),

    default_directive: $ => seq(kw("DEFAULT"), $.sql_expression, ";"),

    not_null_directive: $ => seq(kw("NOT"), kw("NULL"), ";"),

    check_directive: $ => seq(
      optional(seq(kw("CONSTRAINT"), $.identifier)),
      kw("CHECK"), "(", $.sql_expression, ")", ";",
    ),

    mapping_directive: $ => seq(
      kw("MAPPING"), kw("FOR"), commaSep1($.identifier),
      kw("WITH"), commaSep1($.identifier), ";",
    ),

    // OPERATOR FAMILY { } loose members (RFC §14.4) — real PG's own
    // ALTER OPERATOR FAMILY ... ADD list-item grammar, minus the
    // ALTER/family header. Each item restates its own OPERATOR/FUNCTION
    // keyword and optionally ends in "," (more members follow, matching
    // the block's usual comma-separated-list style) or ";" (like every
    // other block directive) — the terminator is optional so the block's
    // last member doesn't need trailing punctuation before "}".
    opfamily_operator_member: $ => seq(
      kw("OPERATOR"), $.number_literal,
      $._operator_symbol,
      "(", $.sql_type, ",", $.sql_type, ")",
      optional(seq(kw("FOR"), choice(
        kw("SEARCH"),
        seq(kw("ORDER"), kw("BY"), $._object_ref),
      ))),
      optional(choice(",", ";")),
    ),

    opfamily_function_member: $ => seq(
      kw("FUNCTION"), $.number_literal,
      optional(seq("(", $.sql_type, optional(seq(",", $.sql_type)), ")")),
      field("name", $._object_ref),
      "(", commaSep($.sql_type), ")",
      optional(choice(",", ";")),
    ),

    // A (possibly schema-qualified) operator symbol — real PG operator
    // characters, see the "Operators" chapter of the PG docs:
    // + - * / < > = ~ ! @ # % ^ & | ` ?
    _operator_symbol: $ => seq(
      optional(seq($.identifier, ".")),
      /[-+*\/<>=~!@#%^&|`?]+/,
    ),

    indexes_block: $ => seq(
      kw("INDEXES"), "{", repeat($.index_def), "}",
    ),

    index_def: $ => seq(
      field("name", $.identifier),
      optional(choice(kw("UNIQUE"), kw("FULLTEXT"), kw("SPATIAL"))),
      optional(seq(kw("USING"), $.identifier)),
      "(", commaSep1($.index_column), ")",
      optional(seq(kw("WHERE"), $.sql_expression)),
      optional(seq(kw("WITH"), "(", commaSep1($.option_pair), ")")),
      ";",
    ),

    index_column: $ => seq(
      $.identifier,
      optional(choice(kw("ASC"), kw("DESC"))),
      optional(seq(kw("NULLS"), choice(kw("FIRST"), kw("LAST")))),
    ),

    policies_block: $ => seq(
      kw("POLICIES"), "{", repeat($.policy_def), "}",
    ),

    policy_def: $ => seq(
      field("name", $.identifier),
      optional(seq(kw("AS"), choice(kw("PERMISSIVE"), kw("RESTRICTIVE")))),
      optional(seq(kw("FOR"), choice(kw("ALL"), kw("SELECT"), kw("INSERT"), kw("UPDATE"), kw("DELETE")))),
      optional(seq(kw("TO"), commaSep1($.identifier))),
      optional(seq(kw("USING"), "(", $.sql_expression, ")")),
      optional(seq(kw("WITH"), kw("CHECK"), "(", $.sql_expression, ")")),
      ";",
    ),

    triggers_block: $ => seq(
      kw("TRIGGERS"), "{", repeat($.trigger_def), "}",
    ),

    trigger_def: $ => seq(
      field("name", $.identifier),
      choice(kw("BEFORE"), kw("AFTER"), seq(kw("INSTEAD"), kw("OF"))),
      commaSep1(choice(kw("INSERT"), kw("UPDATE"), kw("DELETE"), kw("TRUNCATE"))),
      optional(seq(kw("OF"), commaSep1($.identifier))),
      kw("ON"), $._object_ref,
      optional(choice(
        seq(kw("FOR"), kw("EACH"), kw("ROW")),
        seq(kw("FOR"), kw("EACH"), kw("STATEMENT")),
      )),
      optional(seq(kw("WHEN"), "(", $.sql_expression, ")")),
      kw("EXECUTE"), kw("FUNCTION"), $._object_ref, "(", optional($.sql_expression), ")",
      ";",
    ),

    columns_block: $ => seq(
      kw("COLUMNS"), "{", repeat($.column_block), "}",
    ),

    column_block: $ => seq(
      optional(kw("COLUMN")),
      field("name", $.identifier),
      "{",
      repeat($._column_block_directive),
      "}",
    ),

    // Composite type attribute blocks: ATTRIBUTE name { ... } or
    // ATTRIBUTES { name { ... } ... }.
    attributes_block: $ => seq(
      kw("ATTRIBUTES"), "{", repeat($.attribute_entry), "}",
    ),

    attribute_block: $ => seq(
      kw("ATTRIBUTE"),
      field("name", $.identifier),
      "{",
      repeat($._attribute_block_directive),
      "}",
    ),

    attribute_entry: $ => seq(
      field("name", $.identifier),
      "{",
      repeat($._attribute_block_directive),
      "}",
    ),

    _attribute_block_directive: $ => choice(
      $.comment_directive,
      $.renamed_from_directive,
    ),

    _column_block_directive: $ => choice(
      $.comment_directive,
      $.renamed_from_directive,
      $.deprecated_directive,
      seq(kw("STATISTICS"), $.number_literal, ";"),
      seq(kw("STORAGE"), $.identifier, ";"),
      seq(kw("COMPRESSION"), $.identifier, ";"),
      seq(kw("USING"), $.sql_expression, ";"),
      $.name_map_directive,
      $.name_maps_block,
    ),

    constraints_block: $ => seq(
      kw("CONSTRAINTS"), "{", repeat($.constraint_def), "}",
    ),

    constraint_def: $ => seq(
      optional(seq(kw("CONSTRAINT"), $.identifier)),
      $.sql_expression,
      ";",
    ),

    grants_block: $ => seq(
      kw("GRANTS"), "{", repeat($.grant_directive), "}",
    ),

    grant_directive: $ => seq(
      field("privilege", $.privilege_spec),
      kw("TO"), commaSep1(choice(kw("PUBLIC"), $.identifier)),
      optional(seq(kw("WITH"), kw("GRANT"), kw("OPTION"))),
      ";",
    ),

    revokes_block: $ => seq(
      kw("REVOKES"), "{", repeat($.revoke_directive), "}",
    ),

    revoke_directive: $ => seq(
      field("privilege", $.privilege_spec),
      kw("FROM"), commaSep1(choice(kw("PUBLIC"), $.identifier)),
      optional(kw("CASCADE")),
      ";",
    ),

    privilege_spec: $ => seq(
      choice(
        seq(kw("ALL"), optional(kw("PRIVILEGES"))),
        kw("SELECT"), kw("INSERT"), kw("UPDATE"), kw("DELETE"),
        kw("TRUNCATE"), kw("REFERENCES"), kw("TRIGGER"),
        kw("USAGE"), kw("EXECUTE"), kw("CREATE"), kw("CONNECT"), kw("TEMPORARY"),
      ),
      optional(seq(
        kw("ON"),
        optional(choice(
          kw("TABLES"), kw("SEQUENCES"), kw("FUNCTIONS"), kw("ROUTINES"),
          kw("TYPE"), kw("SCHEMA"), kw("DATABASE"),
        )),
      )),
    ),

    partitions_block: $ => seq(
      kw("PARTITIONS"), "{", repeat($.partition_def), "}",
    ),

    partition_def: $ => seq(
      field("name", $.identifier),
      optional(seq(kw("FOR"), kw("VALUES"), kw("IN"), "(", commaSep1($.sql_expression), ")")),
      optional(seq(kw("FOR"), kw("VALUES"), kw("FROM"), "(", commaSep1($.sql_expression), ")",
                   kw("TO"), "(", commaSep1($.sql_expression), ")")),
      optional(kw("DEFAULT")),
      ";",
    ),

    migrate_remove_directive: $ => seq(
      kw("MIGRATE"), kw("REMOVE"),
      "(", field("value", $.string_literal), ")",
      "{",
      repeat(seq($.sql_expression, ";")),
      "}",
    ),

    // NAME MAP [tool] TO <rule|"LiteralName"> ;
    name_map_directive: $ => seq(
      kw("NAME"), kw("MAP"),
      optional(field("tool", $.identifier)),
      kw("TO"),
      field("value", choice($.identifier, $.string_literal)),
      ";",
    ),

    // NAME MAPS { <tool> TO <rule|"LiteralName"> ; ... }
    name_maps_block: $ => seq(
      kw("NAME"), kw("MAPS"),
      "{",
      repeat($.name_map_entry),
      "}",
    ),

    name_map_entry: $ => seq(
      field("tool", $.identifier),
      kw("TO"),
      field("value", choice($.identifier, $.string_literal)),
      ";",
    ),

    // ─── Shared helpers ───────────────────────────────────────────────────────

    _object_ref: $ => choice(
      seq($.identifier, ".", $.identifier, ".", $.identifier),
      seq($.identifier, ".", $.identifier),
      $.identifier,
    ),

    sql_type: $ => seq(
      $._object_ref,
      optional(seq("(", commaSep1($.number_literal), ")")),
      optional(seq("[", optional($.number_literal), "]")),
      optional(seq(kw("WITH"), kw("TIME"), kw("ZONE"))),
      optional(seq(kw("WITHOUT"), kw("TIME"), kw("ZONE"))),
    ),

    // Opaque SQL expression: consumes tokens until top-level { ; or , (comma breaks
    // out so it can be used inside commaSep contexts).  $.identifier is listed
    // explicitly so that word-shaped tokens (SELECT, FROM, WHERE, …) are accepted
    // even when they share spelling with DPG keywords that have lower GLR priority
    // in this position.
    sql_expression: $ => prec.left(repeat1(
      choice(
        $.identifier,
        $.string_literal,
        $.dollar_quoted_string,
        $.number_literal,
        $.boolean_literal,
        seq("(", optional($.sql_expression), ")"),
        /[^(){};,'"$/\s]+/,
        "/",
        "$",
      ),
    )),

    // ─── Macro helpers ────────────────────────────────────────────────────────

    _paren_body: $ => repeat1(choice(
      $.column_def,
      $.table_constraint,
      $.macro_spread,
      ",",
    )),

    _brace_body: $ => repeat1(choice(
      $._block_directive,
      $.macro_spread,
    )),

    // ─── Atoms ────────────────────────────────────────────────────────────────

    identifier: _ => /[a-zA-Z_][a-zA-Z0-9_$]*/,

    // DPG uses single-quoted strings for directive messages (COMMENT, DEPRECATED)
    // matching real PostgreSQL string literal syntax.  Double-quoted strings for
    // identifiers (OWNER, NAME MAP literals) are also accepted as string_literal.
    string_literal: _ => choice(
      seq("'", /[^']*/, "'"),
      seq('"', /[^"]*/, '"'),
    ),

    number_literal: _ => /-?[0-9]+(\.[0-9]+)?/,

    boolean_literal: _ => choice(kw("TRUE"), kw("FALSE"), kw("NULL")),

    line_comment: _ => token(seq("--", /.*/)),

    // The external scanner consumes "/*" content AND the closing "*/".
    block_comment: $ => seq("/*", $._block_comment_content),
  },
});

function kw(word) {
  return alias(reserved(word), word);
}

function reserved(word) {
  return token(prec(1, new RegExp(word, "i")));
}

function commaSep1(rule) {
  return seq(rule, repeat(seq(",", rule)));
}

function commaSep(rule) {
  return optional(commaSep1(rule));
}
