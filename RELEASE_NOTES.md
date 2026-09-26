# Agent Multiplexer v0.1.27

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
