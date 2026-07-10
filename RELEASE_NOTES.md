# Agent Multiplexer v0.1.7

Terminal transport diagnostics.

- Detects when `shpool attach` exits while the selected session is still live.
- Reports that terminal transport was interrupted or wedged instead of silently returning to the shell.
- Reminds users that the agent may still be running before they kill the persistent session.
- Keeps the existing no-implicit-force and explicit `--force` safety behavior.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.7/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.7/scripts/install.sh | bash -s -- --with-codex-lb
```
