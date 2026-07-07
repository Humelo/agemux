# Agent Multiplexer v0.1.3

Account management follow-up release.

- Adds Codex account management actions: `current`, `change`, `login`, `status`, `delete`, and `refresh`.
- Adds `l`, `s`, `r`, and `d` actions to the Codex account picker for login/update, status, usage refresh, and delete.
- Standardizes account deletion on `d delete` in both Codex and Claude account pickers while keeping `k`/`x` as compatibility aliases.
- Adds `agemux claude-accounts delete <selector>`.
- Keeps deletion local-only: it removes the local account slot/config mapping and does not revoke provider-side tokens.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.3/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.3/scripts/install.sh | bash -s -- --with-codex-lb
```
