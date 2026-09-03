# Agent Multiplexer

Agent Multiplexer is a local CLI for people who run multiple Claude Code, Codex, and Grok Build sessions on a shared workstation or remote server.

It ships one main command:

- `agemux`: persistent Codex, Claude, and Grok session picker backed by `shpool`, with account views built in

The implementation is written in Go and ships as standalone binaries.

## Install

Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.21/scripts/install.sh | bash
```

Install and make bare `claude` use the selected Claude account:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.21/scripts/install.sh | bash -s -- --install-claude-shim
```

Optionally install or upgrade the companion `codex-lb` tool through `uv`:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.21/scripts/install.sh | bash -s -- --with-codex-lb
```

Windows PowerShell:

```powershell
iwr https://raw.githubusercontent.com/Humelo/agemux/v0.1.21/scripts/install.ps1 -UseB | iex
```

On native Windows, Claude account management is supported. Persistent Agent Multiplexer sessions require POSIX PTY support and `shpool`, so use them from WSL, Linux, or macOS.

## Usage

```sh
agemux
agemux codex
agemux codex-new
agemux claude
agemux claude-new
agemux grok
agemux grok-new
agemux start codex nightly-review --resume SESSION_UUID --background --root /workspace/project
agemux start grok nightly-grok --resume SESSION_UUID --background --root /workspace/project
printf '%s' 'Review the pending queue.' | agemux send nightly-review
agemux capture nightly-review --lines 120
agemux codex-accounts
agemux codex-accounts new
agemux codex-accounts change 2
agemux codex-accounts delete 2
agemux claude-accounts
agemux claude-accounts list
agemux grok-accounts
agemux grok-accounts list
agemux list
agemux attach NAME
agemux detach NAME
agemux restart NAME
agemux attach --force NAME
agemux kill NAME
```

`agemux` opens a persistent session picker:

- `c`: new Codex resume picker
- `C`: new Codex session
- `l`: new Claude resume picker
- `L`: new Claude session
- `g`: Grok resume picker (lists this directory's top-level Grok sessions, then launches `grok --resume`)
- `G`: new Grok session (`grok --session-id` so it does not open the welcome picker)
- `Enter` on `Codex accounts`: switch the active Codex CLI auth file or choose `+ Add Codex account`
- `Enter` on `Claude accounts`: open the Claude account picker
- `Enter` on `Grok accounts`: switch the active Grok CLI auth file or choose `+ Add Grok account`
- `d`: detach the selected terminal while keeping its agent session running
- `r`: restart the selected Codex or Grok session on its exact session UUID
- `k`: kill selected persistent session after confirmation

Close the terminal tab to detach. The underlying session keeps running in `shpool`.

Named Codex and Grok sessions can be started without attaching a terminal. `agemux send` delivers one submitted prompt to the session's PTY, and `agemux capture` reads recent terminal output for health checks. Sending works while the shpool session is attached or detached and does not take over another terminal attachment.

```sh
agemux start codex nightly-review \
  --resume SESSION_UUID \
  --background \
  --root /workspace/project \
  --model gpt-5.6-sol \
  --effort high \
  --service-tier default \
  --config notice.hide_rate_limit_model_nudge=true

agemux start grok nightly-grok \
  --resume SESSION_UUID \
  --background \
  --root /workspace/project \
  --model grok-4.6 \
  --effort xhigh

agemux send nightly-review "Continue the scheduled review."
agemux send nightly-review --file /path/to/prompt.txt
agemux capture nightly-review --lines 200
```

The control channel is a same-user Unix socket stored under `$XDG_RUNTIME_DIR/agemux` or `~/.local/run/agemux`, with directory mode `0700` and socket mode `0600`. Treat access to the local account as permission to control these agent sessions.

Sessions that are already attached in another terminal are not force-detached by default. Close the old terminal first, or use `agemux attach --force NAME` when you intentionally want to take over an attached session.

If `shpool attach` exits while the session is still live and disconnected, agemux automatically reconnects up to three times with bounded backoff. The retry budget resets after a stable minute so isolated interruptions do not accumulate over a long-running terminal. agemux also resets bracketed paste, focus tracking, modify-other-keys, mouse tracking, and the full Kitty keyboard-protocol stack at TUI and attachment boundaries so a crashed nested CLI does not leak escape sequences into the shell. Grok attach restores the alternate screen and mouse modes before connecting, because shpool's screen restore replays cells only and a background-started Grok session will not re-emit those modes.

`agemux restart NAME` and the interactive `r` key stop the selected Codex or Grok process and immediately resume the same session by UUID. The confirmation shows the internal agemux session name and verified session UUID so a changing display title cannot hide the target identity. This bypasses the slower resume picker and preserves the session root, title, model, and reasoning effort stored by agemux. Codex restarts also keep service tier and extra Codex config. Restart ignores open subagent rollout files and is limited to sessions whose exact session UUID can be verified before the old process is stopped. Grok session IDs are read from `~/.grok/active_sessions.json` for the live Grok process. Resume starts and restarts are serialized by UUID and refuse a session already owned by another live or starting agemux session. Sessions selected through the interactive picker are checked again as soon as their session UUID becomes observable; a duplicate picker session is closed instead of claiming the same session.

When an explicit kill finds a stale disconnected shpool entry whose child process has already exited, agemux repairs the stale entry and retries the kill once. Attached sessions do not enter this recovery path.

Codex account switcher:

```sh
agemux codex-accounts
agemux codex-accounts list
agemux codex-accounts current
agemux codex-accounts change 2
agemux codex-accounts new
agemux codex-accounts new work
agemux codex-accounts login work
agemux codex-accounts status work
agemux codex-accounts delete work
```

`+ Add Codex account` starts `codex login` in an isolated temporary `CODEX_HOME`, then saves the result as `~/.codex/auth.<name>.json` and switches the active `~/.codex/auth.json` to it.

Inside the Codex account picker, use `Enter` to switch, `n` to add, `l` to login/update, `s` for status, `r` to refresh usage, and `d` to delete a local account slot.

Claude account switcher:

```sh
agemux claude-accounts
agemux claude-accounts list
agemux claude-accounts change 2
agemux claude-accounts current
agemux claude-accounts new
agemux claude-accounts login 2
agemux claude-accounts status 2
agemux claude-accounts delete 2
```

The Claude account picker also includes a `+ Add Claude account` row for creating a new account slot and starting login.
Inside the Claude account picker, use `Enter` to switch, `n` to add, `l` to login, `s` for status, `r` to refresh usage, and `d` to delete a local account slot.

By default, `agemux claude-accounts change` changes the current account for Claude runs launched through Agent Multiplexer and for a `claude` shim installed with:

```sh
agemux claude-accounts install-shim --force
```

Without the shim, a plain `claude` command uses Claude Code's default config directory.

Grok account switcher:

```sh
agemux grok-accounts
agemux grok-accounts list
agemux grok-accounts current
agemux grok-accounts change 2
agemux grok-accounts new
agemux grok-accounts login 2
agemux grok-accounts status 2
agemux grok-accounts delete 2
```

`+ Add Grok account` starts `grok login` in an isolated temporary `GROK_HOME`, then saves the result as `~/.grok/auth.<name>.json` and switches the active `~/.grok/auth.json` to it. If only `auth.json` exists, the first list/import promotes it to a named slot.

Inside the Grok account picker, use `Enter` to switch, `n` to add, `l` to login/update, `s` for status, and `d` to delete a local account slot.

## Requirements

- `shpool` for `agemux`
- Claude Code CLI for Claude sessions and account management
- Codex CLI for Codex sessions
- Grok Build CLI for Grok sessions
- `uv` only if you opt in to installing the companion `codex-lb` tool with `--with-codex-lb`. The installer installs `uv` if it is missing.

`agemux` launches your local `codex`, `claude`, and `grok` commands. It does not bundle or proxy any provider's service.

When a Claude launch does not inherit a provider environment (for example from
a non-interactive automation shell), agemux sources the mode-600
`~/.config/sub2api/claude.env` file when it exists. Set
`AGEMUX_CLAUDE_ENV_FILE` to use a different provider env file. The file path,
not its credential contents, is passed through the shpool command, so provider
secrets are not embedded in session metadata or command lines.
Loading that file is an intentional caller-provider override: top-level
`agemux claude`/`claude-new` runs the real Claude binary and does not replace
the provider with a selected `CLAUDE_CONFIG_DIR` account profile. Use
`agemux claude-accounts run` when you explicitly want a managed account
profile instead.

## Safety

Agent Multiplexer is a local terminal/session tool, not a hosted proxy, token broker, or quota aggregation service. It runs official local CLIs using local configuration that you control. Use it only with accounts and credentials you are authorized to operate, and follow the applicable provider terms and your organization policy.

Top-level `agemux claude` and `agemux claude-new` preserve a caller-supplied Claude provider configuration such as a custom base URL or API credential environment. In that mode they run the resolved local `claude` binary with the caller's default Claude configuration; credentials remain in the normal Claude config or its configured helper and are never placed in the shpool command line. Without a caller-supplied provider configuration, top-level Claude launches continue to use the selected agemux Claude account profile. Use `agemux claude-accounts run` when you explicitly want to select a managed `CLAUDE_CONFIG_DIR` profile.

Agent Multiplexer does not store Claude, Codex, or Grok tokens in its own state files. It stores local config directory paths, cached account status, cached usage data, and persistent session metadata. Cached Claude usage data can include local Claude Code status fields such as session identifiers, model names, and context-window metadata. Codex and Grok account switching copy an existing local auth file into the CLI's active auth path; they do not log out, revoke tokens, or change provider-side limits.

Agent sessions launched by `agemux` use the official CLI dangerous permission bypass flags by default:

- Codex: `--dangerously-bypass-approvals-and-sandbox`
- Claude: `--dangerously-skip-permissions`
- Grok: `--always-approve`

Use Agent Multiplexer this way only on trusted local machines or disposable sandboxes. To disable the bypass flags:

```sh
AGEMUX_CODEX_DANGEROUS=0 agemux codex
AGEMUX_CLAUDE_DANGEROUS=0 agemux claude
AGEMUX_GROK_DANGEROUS=0 agemux grok
```

## Data

Local state is stored under:

- `~/.local/share/agemux`
- a Claude account state directory in your user data folder

## Development

```sh
go test ./...
python3 tests/smoke.py
```

Build local binaries:

```sh
go build -o dist/agemux ./cmd/agemux
```
