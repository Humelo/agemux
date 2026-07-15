# Agent Multiplexer v0.1.9

Automatic recovery for interrupted shpool terminal transports.

- Reconnects automatically when shpool exits with status 1 but the agent session remains live and disconnected.
- Waits for shpool's delayed `attached` to `disconnected` state transition before deciding whether recovery is safe.
- Uses bounded exponential backoff and stops after five consecutive failures.
- Resets the retry budget after a stable minute so isolated failures do not accumulate over long-running terminals.
- Does not force-detach sessions that another client still owns and does not retry sessions that have exited.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.9/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.9/scripts/install.sh | bash -s -- --with-codex-lb
```
