# Agent Multiplexer v0.1.21

Preserve caller-configured Claude provider environments in persistent sessions.

- Claude sessions launched with a caller-supplied provider configuration (for example a custom base URL or API credential environment) now run the resolved local Claude CLI with that configuration intact.
- Credentials remain in the normal Claude configuration or its configured helper and are never embedded in the shpool command line.
- When no provider override is present, top-level Claude launches continue to use the selected agemux Claude account profile.
- Added regression coverage for both launch modes and for provider-environment redaction.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.21/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.21/scripts/install.sh | bash -s -- --with-codex-lb
```
