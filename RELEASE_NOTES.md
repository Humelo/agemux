# Agent Multiplexer v0.1.14

Prevent Codex thread identity collisions in persistent sessions.

- Close a Codex resume-picker session when its selected thread is already owned by another live agemux session.
- Show the internal agemux session name and verified Codex thread UUID before an interactive restart.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.14/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.14/scripts/install.sh | bash -s -- --with-codex-lb
```
