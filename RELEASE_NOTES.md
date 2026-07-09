# Agent Multiplexer v0.1.6

Stale attach safety update.

- Stops default attach from force-detaching sessions that are already attached elsewhere.
- Adds explicit `agemux attach --force NAME` for intentional takeover.
- Adds a timeout around `shpool list --json` so the picker fails clearly instead of hanging forever when the shpool daemon is wedged.
- Keeps disconnected sessions attachable without the risky force-detach path.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.6/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.6/scripts/install.sh | bash -s -- --with-codex-lb
```
