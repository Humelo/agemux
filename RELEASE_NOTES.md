# Agent Multiplexer v0.1.28

Track the exact Codex thread when the TUI uses a shared local background server.

- Give each new Codex runner a private Unix transport and observe successful
  root-thread start, resume and fork responses from that runner alone.
- Stream conversation traffic unchanged with bounded identity inspection; never
  identify a thread by its title, the newest file, or a shared daemon file handle.
- Preserve per-session configuration and existing ownership checks. Older CLIs
  keep direct file-descriptor tracking. Set `AGEMUX_CODEX_DAEMON_TRACKING=0` to
  retain the legacy transport explicitly.
- Releasing or installing agemux does not restart shpool, Codex, or existing
  runners. New tracking applies when a runner is next started.

## v0.1.27

Add named selection-key control and runner capability discovery for integrations.

- `agemux control-info NAME` returns the runner's supported control operations.
- `agemux keys NAME KEY...` sends validated selection keys directly, without
  bracketed paste. Supported keys include arrows, Enter, Escape, Tab, `s`, and Backspace.
- Socket clients can use the corresponding `info` and `keys` operations.
- Invalid or oversized key batches are rejected before writing any input.
- Existing text submission, output capture, same-user socket permissions and
  persistent session behavior are preserved. Running runners are not replaced.

## v0.1.26

Fix dependency discovery for non-interactive SSH and service invocations.

- Preserve explicit `AGEMUX_SHPOOL_BIN` and `PATH` precedence.
- Discover an installed shpool next to agemux or in the standard user bin directories when the caller has a minimal PATH.
- Reject directory and non-executable fallback candidates.
- Cover lookup, override precedence and invocation with a deliberately restricted PATH.

Existing persistent sessions and the shpool daemon are unchanged.
