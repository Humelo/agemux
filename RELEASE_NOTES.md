# Agent Multiplexer v0.1.26

Fix dependency discovery for non-interactive SSH and service invocations.

- Preserve explicit `AGEMUX_SHPOOL_BIN` and `PATH` precedence.
- Discover an installed shpool next to agemux or in the standard user bin directories when the caller has a minimal PATH.
- Reject directory and non-executable fallback candidates.
- Cover lookup, override precedence and invocation with a deliberately restricted PATH.

Existing persistent sessions and the shpool daemon are unchanged.
