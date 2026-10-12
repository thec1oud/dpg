---
title: "CLI Reference"
description: "All dpg commands with flags and examples."
weight: 7
---

The `dpg` CLI is the primary interface to the DPG compiler and migration engine. All commands accept the following global flags:

| Flag | Short | Description |
|------|-------|-------------|
| `--dir` | `-C` | Project root directory (default: current working directory) |
| `--env` | | Path to `.env` file (default: `.env` in project root, if present) |

## Commands

| Command | Description |
|---------|-------------|
| [dpg plan](dpg_plan/) | Diff desired state vs snapshot and print the SQL migration |
| [dpg apply](dpg_apply/) | Execute the planned migration and update the snapshot |
| [dpg verify](dpg_verify/) | Check the live database for drift against the snapshot |
| [dpg resolve](dpg_resolve/) | Adopt, revert, ignore or acknowledge drift items |
| [dpg log](dpg_log/) | Repair and rebase the migration log |
| [dpg dump](dpg_dump/) | Introspect a live database and produce initial `.dpg` source files |
| [dpg diff](dpg_diff/) | Diff two DPG source directories and print the SQL migration |
| [dpg fmt](dpg_fmt/) | Format `.dpg` source files in place |
| [dpg validate](dpg_validate/) | Validate `.dpg` source files offline without diffing |
| [dpg portability](dpg_portability/) | Report PostgreSQL-specific constructs in use |
| [dpg init](dpg_init/) | Scaffold a new DPG project |
| [dpg completion](dpg_completion/) | Generate shell autocompletion scripts |
| [dpg docs](dpg_docs/) | Serve the embedded documentation locally |

The pages in this section are auto-generated from the CLI source via `make docs-cli`.
