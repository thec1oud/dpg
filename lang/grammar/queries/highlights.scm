; Comments
(line_comment) @comment
(block_comment) @comment

; Object-type declaration keywords
(table_declaration "TABLE" @keyword.type)
(view_declaration "VIEW" @keyword.type)
(materialized_view_declaration "MATERIALIZED" @keyword.type "VIEW" @keyword.type)
(recursive_view_declaration "RECURSIVE" @keyword.type "VIEW" @keyword.type)
(function_declaration "FUNCTION" @keyword.type)
(procedure_declaration "PROCEDURE" @keyword.type)
(aggregate_declaration "AGGREGATE" @keyword.type)
(enum_declaration ["TYPE" "ENUM"] @keyword.type)
(composite_type_declaration "TYPE" @keyword.type)
(range_type_declaration "TYPE" @keyword.type)
(base_type_declaration "TYPE" @keyword.type)
(domain_declaration "DOMAIN" @keyword.type)
(virtual_type_declaration "VIRTUAL" @keyword.type "TYPE" @keyword.type)
(sequence_declaration "SEQUENCE" @keyword.type)
(role_declaration "ROLE" @keyword.type)
(tablespace_declaration "TABLESPACE" @keyword.type)
(schema_block "SCHEMA" @keyword.type)
(schema_declaration "SCHEMA" @keyword.type)
(extension_declaration "EXTENSION" @keyword.type)
(publication_declaration "PUBLICATION" @keyword.type)
(subscription_declaration "SUBSCRIPTION" @keyword.type)
(event_trigger_declaration "EVENT" @keyword.type "TRIGGER" @keyword.type)
(default_privileges_declaration "DEFAULT" @keyword.type "PRIVILEGES" @keyword.type)
(fdw_declaration "FOREIGN" @keyword.type "DATA" @keyword.type "WRAPPER" @keyword.type)
(foreign_server_declaration "SERVER" @keyword.type)
(user_mapping_declaration "USER" @keyword.type "MAPPING" @keyword.type)
(text_search_config_declaration "TEXT" @keyword.type "SEARCH" @keyword.type "CONFIGURATION" @keyword.type)
(text_search_dict_declaration "TEXT" @keyword.type "SEARCH" @keyword.type "DICTIONARY" @keyword.type)
(text_search_parser_declaration "TEXT" @keyword.type "SEARCH" @keyword.type "PARSER" @keyword.type)
(text_search_template_declaration "TEXT" @keyword.type "SEARCH" @keyword.type "TEMPLATE" @keyword.type)
(collation_declaration "COLLATION" @keyword.type)
(operator_declaration "OPERATOR" @keyword.type)
(operator_class_declaration "OPERATOR" @keyword.type "CLASS" @keyword.type)
(operator_family_declaration "OPERATOR" @keyword.type "FAMILY" @keyword.type)
(cast_declaration "CAST" @keyword.type)
(statistics_declaration "STATISTICS" @keyword.type)
(macro_declaration "MACRO" @keyword.type)

; Object names
(table_declaration name: (_) @type)
(view_declaration name: (_) @type)
(materialized_view_declaration name: (_) @type)
(recursive_view_declaration name: (_) @type)
(function_declaration name: (_) @function)
(procedure_declaration name: (_) @function)
(aggregate_declaration name: (_) @function)
(enum_declaration name: (_) @type)
(composite_type_declaration name: (_) @type)
(range_type_declaration name: (_) @type)
(domain_declaration name: (_) @type)
(virtual_type_declaration name: (_) @type)
(sequence_declaration name: (_) @type)
(role_declaration name: (identifier) @variable)
(schema_block name: (identifier) @module)
(macro_declaration name: (identifier) @keyword.macro)

; Column definitions
(column_def name: (identifier) @variable.member)

; Block directive keywords
(comment_directive "COMMENT" @keyword.directive)
(owner_directive "OWNER" @keyword.directive)
(renamed_from_directive "RENAMED" @keyword.directive "FROM" @keyword.directive)
(protected_directive "PROTECTED" @keyword.directive)
(deprecated_directive "DEPRECATED" @keyword.directive)
(drop_cascade_directive "DROP" @keyword.directive "CASCADE" @keyword.directive)
(migrate_remove_directive "MIGRATE" @keyword.directive "REMOVE" @keyword.directive)
(default_directive "DEFAULT" @keyword.directive)
(not_null_directive "NOT" @keyword.directive "NULL" @keyword.directive)
(check_directive "CHECK" @keyword.directive)
(mapping_directive "MAPPING" @keyword.directive)
(opfamily_operator_member "OPERATOR" @keyword.directive)
(opfamily_operator_member "FOR" @keyword.directive)
(opfamily_operator_member "SEARCH" @keyword.directive)
(opfamily_operator_member "ORDER" @keyword.directive)
(opfamily_operator_member "BY" @keyword.directive)
(opfamily_function_member "FUNCTION" @keyword.directive)
(opfamily_function_member name: (identifier) @function)
(name_map_directive "NAME" @keyword.directive "MAP" @keyword.directive)
(name_maps_block "NAME" @keyword.directive "MAPS" @keyword.directive)

; Name map entries — tool names and target values
(name_map_directive tool: (identifier) @variable)
(name_map_directive value: (identifier) @constant)
(name_map_directive value: (string_literal) @string)
(name_map_entry tool: (identifier) @variable)
(name_map_entry value: (identifier) @constant)
(name_map_entry value: (string_literal) @string)

; Nested block section keywords
(indexes_block "INDEXES" @keyword.block)
(policies_block "POLICIES" @keyword.block)
(triggers_block "TRIGGERS" @keyword.block)
(columns_block "COLUMNS" @keyword.block)
(constraints_block "CONSTRAINTS" @keyword.block)
(grants_block "GRANTS" @keyword.block)
(revokes_block "REVOKES" @keyword.block)
(attributes_block "ATTRIBUTES" @keyword.block)
(attribute_block "ATTRIBUTE" @keyword.block)
(enum_value_rename_directive "VALUE" @keyword.block)
(partitions_block "PARTITIONS" @keyword.block)

; DPG block braces
(dpg_block "{" @punctuation.bracket)
(dpg_block "}" @punctuation.bracket)

; Macro spread
(macro_spread "..." @operator)
(macro_spread name: (identifier) @keyword.macro)

; Literals
(string_literal) @string
(dollar_quoted_string) @string
(number_literal) @number
(boolean_literal) @constant.builtin

; Column constraint keywords
"NOT" @keyword
"NULL" @keyword
"UNIQUE" @keyword
"PRIMARY" @keyword
"KEY" @keyword
"DEFAULT" @keyword
"REFERENCES" @keyword
"GENERATED" @keyword
"ALWAYS" @keyword
"IDENTITY" @keyword
"CHECK" @keyword
"CONSTRAINT" @keyword
"FOREIGN" @keyword
"RETURNS" @keyword
"LANGUAGE" @keyword
"AS" @keyword
"WITH" @keyword
"FOR" @keyword
"TO" @keyword
"FROM" @keyword
"ON" @keyword
"IN" @keyword
"USING" @keyword

; Function option keywords
"IMMUTABLE" @keyword.modifier
"STABLE" @keyword.modifier
"VOLATILE" @keyword.modifier
"STRICT" @keyword.modifier
"SECURITY" @keyword.modifier
"DEFINER" @keyword.modifier
"PARALLEL" @keyword.modifier

; Punctuation
"(" @punctuation.bracket
")" @punctuation.bracket
"{" @punctuation.bracket
"}" @punctuation.bracket
"," @punctuation.delimiter
";" @punctuation.delimiter
"." @punctuation.delimiter
