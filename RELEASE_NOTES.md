# Agent Multiplexer v0.1.4

Codex account backup visibility fix.

- Hides internal `auth.backup-*.json` safety backups from the Codex account picker and CLI list.
- Reserves the `backup-` Codex account name prefix so future account slots cannot collide with internal backup files.
- Keeps the backup safety behavior itself: when switching away from an unmanaged active `auth.json`, Agent Multiplexer still preserves it locally instead of overwriting it silently.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.4/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.4/scripts/install.sh | bash -s -- --with-codex-lb
```
