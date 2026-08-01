# Agent Multiplexer v0.1.13

Reliable terminal recovery and exact Codex session restarts.

- Preserve batched, fragmented, and enhanced terminal cursor-key sequences across the session and account pickers.
- Reset nested keyboard protocols and restore terminal state after normal exits, transport interruptions, and signals.
- Retry transient shpool attachment disconnects with bounded backoff.
- Restart a Codex session directly on its verified root thread UUID while ignoring open subagent rollouts and preserving launch settings.
- Serialize resume starts and restarts by UUID, refusing the operation when another live or starting agemux session already owns the thread.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.13/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.13/scripts/install.sh | bash -s -- --with-codex-lb
```
