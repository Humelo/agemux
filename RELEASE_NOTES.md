# Agent Multiplexer v0.1.1

Public-ready release.

- Adds `agemux`, a persistent Codex and Claude session picker backed by `shpool`, with embedded account management views.
- Adds account entries in `agemux` for Claude account management and Codex auth-file switching.
- Adds Claude account usage refresh, statusline support, and optional `claude` shim installation through `agemux claude-accounts`.
- Keeps `codex-lb` installation opt-in with `--with-codex-lb`.
- Clarifies that Agent Multiplexer is a local session/configuration tool, not a hosted proxy, token broker, or quota aggregation service.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.1/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.1/scripts/install.sh | bash -s -- --with-codex-lb
```
