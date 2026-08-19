# Agent Multiplexer v0.1.20

Harden terminal attachment boundaries and reduce Claude account startup memory.

- Foreground attachments now start on a fresh terminal line, preventing a restored Codex screen from being appended to the shell's `agemux` command text.
- Managed Claude wrapper detection now reads only the 4 KiB script header instead of loading a large Claude executable into memory.

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.20/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.20/scripts/install.sh | bash -s -- --with-codex-lb
```
