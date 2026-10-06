# OpenCode — DEPRECATED (v19.0.10)

**Status:** DEPRECATED — replaced by Pi-Bolt in v19.0.10.

**Rollback:** This file (`config.json.DEPRECATED`) is preserved for rollback.
If Pi-Bolt fails acceptance tests, restore OpenCode by renaming this file
back to `config.json` and reverting the migration commit.

**Why deprecated:** Per the OpenCode→Pi-Bolt migration guide, OpenCode is
removed only after Pi-Bolt passes all acceptance tests. Since the repo-level
migration (config template + install.sh integration + docs) is done but the
real Pi-Bolt binary test requires a GPU host, this config is quarantined
rather than deleted — preserving rollback capability.

**Original config preserved at:** `agents/opencode/config.json.DEPRECATED`

**Migration documentation:** `docs/SCARLIX_AGENT_MIGRATION_OpenCode_to_PiBolt.md`

**Pi-Bolt replacement config:** `agents/pi-bolt/config.template.json`
