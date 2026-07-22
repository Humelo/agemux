# Agent Multiplexer v0.1.11

Named background Codex sessions and safe local automation controls.

- Start a deterministic Codex session with `agemux start codex NAME`.
- Resume an exact Codex conversation with `--resume SESSION_UUID`.
- Use `--background` to create the shpool session without attaching a terminal.
- Send a submitted prompt with `agemux send NAME`, either as an argument, stdin, or `--file PATH`.
- Read recent PTY output with `agemux capture NAME --lines N` for scheduler health checks.
- Keep external input independent of shpool attachment state, so automation does not take over an attached terminal.
- Protect each local control socket with same-user filesystem permissions.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.11/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.11/scripts/install.sh | bash -s -- --with-codex-lb
```
