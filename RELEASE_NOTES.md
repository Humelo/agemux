# Agent Multiplexer v0.1.16

Fix the startup action grid and add Grok account switching.

- Show the first-screen actions in 3 columns: resume / new / accounts for Codex, Claude, and Grok.
- Add `agemux grok-accounts` with the same add / switch / login / delete flow as Codex, using `~/.grok/auth.<name>.json`.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.16/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.16/scripts/install.sh | bash -s -- --with-codex-lb
```
