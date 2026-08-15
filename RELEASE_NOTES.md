# Agent Multiplexer v0.1.19

Restore Grok internal scroll after a background start.

- Grok attach now enables the alternate screen and mouse modes on the client first. shpool's default screen restore only replays cells, so a `--background` session never re-emits those modes.
- Detach also disables mouse tracking so it does not leak into the shell.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.19/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.19/scripts/install.sh | bash -s -- --with-codex-lb
```
