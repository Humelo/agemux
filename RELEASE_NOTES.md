# Agent Multiplexer v0.1.17

Make the Grok resume picker actually pick a session.

- `agemux grok` now lists this directory's Grok sessions from `~/.grok/sessions` and launches `grok --resume UUID`.
- Grok's welcome screen is no longer used as the picker; it does not reliably open inside shpool.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.17/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.17/scripts/install.sh | bash -s -- --with-codex-lb
```
