# Agent Multiplexer v0.1.10

Non-destructive session detach controls.

- Press `d` on a session in the main picker to detach its terminal without stopping the agent.
- Run `agemux detach NAME` for the same behavior in scripts or another shell.
- Keeps agemux metadata intact so detached sessions remain available for later reattachment.
- Treats already-disconnected sessions as a safe no-op.
- Shows disconnected state explicitly in the session list while retaining confirmed `k` kill behavior.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.10/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.10/scripts/install.sh | bash -s -- --with-codex-lb
```
