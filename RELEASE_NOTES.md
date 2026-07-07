# Agent Multiplexer v0.1.5

Default agent permission behavior update.

- Launches Codex sessions with `--dangerously-bypass-approvals-and-sandbox` by default.
- Launches Claude sessions with `--dangerously-skip-permissions` by default.
- Adds explicit opt-outs: set `AGEMUX_CODEX_DANGEROUS=0` or `AGEMUX_CLAUDE_DANGEROUS=0` to disable the corresponding bypass flag.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.5/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.5/scripts/install.sh | bash -s -- --with-codex-lb
```
