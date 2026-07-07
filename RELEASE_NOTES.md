# Agent Multiplexer v0.1.2

Account picker follow-up release.

- Adds selectable `+ Add Codex account` and `+ Add Claude account` rows in the account pickers.
- Adds `agemux codex-accounts new [name]` to create a new Codex auth slot through an isolated `codex login` flow.
- Keeps Codex auth storage local: new accounts are saved as `~/.codex/auth.<name>.json`, then copied into the active `~/.codex/auth.json` when selected.
- Keeps `codex-lb` installation opt-in with `--with-codex-lb`.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.2/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.2/scripts/install.sh | bash -s -- --with-codex-lb
```
