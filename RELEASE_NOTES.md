# Agent Multiplexer v0.1.8

Terminal input, account safety, and portable release fixes.

- Prevents focus-in escape bytes from leaking into Codex after choosing a resumed session.
- Reassembles fragmented terminal escape sequences so arrow keys remain reliable in all pickers.
- Preserves refreshed Codex credentials in their selected account slot before switching accounts.
- Serializes duplicate Codex and Claude account creation and only selects a new Claude slot after login succeeds.
- Preserves previously unmanaged Codex credentials when switching accounts on Windows.
- Handles very short terminals without refresh-screen panics and renders Unicode account labels by display-cell width.
- Preserves Unicode aliases during account search.
- Refreshes the session list once per second instead of spawning `shpool list` continuously while idle.
- Refuses to kill shpool sessions that are not owned by agemux.
- Builds static, trimmed release binaries for broader Linux compatibility without local build paths.
- Separates the installer destination variable (`AGEMUX_INSTALL_PREFIX`) from the runtime session-name prefix (`AGEMUX_PREFIX`).

## Install on Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.8/scripts/install.sh | bash
```

Opt in to companion `codex-lb` installation:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.8/scripts/install.sh | bash -s -- --with-codex-lb
```
