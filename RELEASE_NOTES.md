# Agent Multiplexer v0.1.15

Add persistent Grok Build sessions alongside Codex and Claude.

- Launch Grok from `agemux grok`, `agemux grok-new`, the interactive `g`/`G` keys, and `agemux start grok`.
- Keep Grok running in `shpool` after the terminal tab closes, then reattach, send, capture, or restart by session UUID.
- Use Grok's welcome screen as the resume picker. Fresh starts pass `grok --session-id UUID`; named resumes and restarts pass `grok --resume UUID`.
- Default Grok launches include `--always-approve`. Set `AGEMUX_GROK_DANGEROUS=0` to disable that flag.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.15/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.15/scripts/install.sh | bash -s -- --with-codex-lb
```
