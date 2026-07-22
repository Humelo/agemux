# Agent Multiplexer v0.1.12

Reliable cleanup for stale disconnected shpool sessions.

- Recover a disconnected shpool entry whose child process has already exited before retrying an explicit kill.
- Keep attached sessions out of the stale-session recovery path.
- Include the underlying command error when shpool exits without diagnostic output.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.12/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.12/scripts/install.sh | bash -s -- --with-codex-lb
```
