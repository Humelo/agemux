# Agent Multiplexer v0.1.23

Fix persistent session creation for all provider launch paths.

- New Codex, Claude, and Grok sessions now pass their generated session name
  to `shpool attach`, so the top-level commands and interactive picker no
  longer fail with shpool's missing `<NAME>` argument error.
- Added regression coverage for every provider create kind.

This release also includes the Claude provider handoff hardening from v0.1.22.

- Claude provider environments now survive the shpool daemon boundary through
  private runtime snapshots without embedding credential values in command
  lines or session metadata.
- Explicit/default env files fail closed when missing, unreadable, invalid, or
  unsafe; stale daemon provider variables are cleared before launch.
- Managed agemux/clsw Claude shims are resolved to the real Claude binary when
  caller provider configuration is active, including symlinked and PATH-based
  commands.
- `agemux claude-new --help` and other bare provider help/argument paths no
  longer create unintended sessions.
- Added regression coverage for provider snapshots, cleanup, directory
  permissions, argument validation, and managed-shim resolution.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.23/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.23/scripts/install.sh | bash -s -- --with-codex-lb
```
