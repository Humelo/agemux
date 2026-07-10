# Agent Multiplexer

Agent Multiplexer is a local CLI for people who run multiple Claude Code and Codex sessions on a shared workstation or remote server.

It ships one main command:

- `agemux`: persistent Codex and Claude session picker backed by `shpool`, with account views built in

The implementation is written in Go and ships as standalone binaries.

## Install

Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.7/scripts/install.sh | bash
```

Install and make bare `claude` use the selected Claude account:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.7/scripts/install.sh | bash -s -- --install-claude-shim
```

Optionally install or upgrade the companion `codex-lb` tool through `uv`:

```sh
curl -fsSL https://raw.githubusercontent.com/Humelo/agemux/v0.1.7/scripts/install.sh | bash -s -- --with-codex-lb
```

Windows PowerShell:

```powershell
iwr https://raw.githubusercontent.com/Humelo/agemux/v0.1.7/scripts/install.ps1 -UseB | iex
```

On native Windows, Claude account management is supported. Persistent Agent Multiplexer sessions require POSIX PTY support and `shpool`, so use them from WSL, Linux, or macOS.

## Usage

```sh
agemux
agemux codex
agemux codex-new
agemux claude
agemux claude-new
agemux codex-accounts
agemux codex-accounts new
agemux codex-accounts change 2
agemux codex-accounts delete 2
agemux claude-accounts
agemux claude-accounts list
agemux list
agemux attach --force NAME
```

`agemux` opens a persistent session picker:

- `c`: new Codex resume picker
- `C`: new Codex session
- `l`: new Claude resume picker
- `L`: new Claude session
- `Enter` on `Codex accounts`: switch the active Codex CLI auth file or choose `+ Add Codex account`
- `Enter` on `Claude accounts`: open the Claude account picker
- `k`: kill selected persistent session after confirmation

Close the terminal tab to detach. The underlying session keeps running in `shpool`.

Sessions that are already attached in another terminal are not force-detached by default. Close the old terminal first, or use `agemux attach --force NAME` when you intentionally want to take over an attached session.

If `shpool attach` exits while the session is still live, agemux reports the terminal transport interruption instead of silently returning to the shell. The agent process may still be running; reopen the session before deciding to kill it.

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

## Requirements

- `shpool` for `agemux`
- Claude Code CLI for Claude sessions and account management
- Codex CLI for Codex sessions
- `uv` only if you opt in to installing the companion `codex-lb` tool with `--with-codex-lb`. The installer installs `uv` if it is missing.

`agemux` launches your local `codex` and `claude` commands. It does not bundle or proxy either provider's service.

## Safety

Agent Multiplexer is a local terminal/session tool, not a hosted proxy, token broker, or quota aggregation service. It runs official local CLIs using local configuration that you control. Use it only with accounts and credentials you are authorized to operate, and follow the applicable provider terms and your organization policy.

Agent Multiplexer does not store Claude or Codex tokens in its own state files. It stores local config directory paths, cached account status, cached usage data, and persistent session metadata. Cached Claude usage data can include local Claude Code status fields such as session identifiers, model names, and context-window metadata. Codex account switching copies an existing local Codex auth file into Codex's active auth path; it does not log out, revoke tokens, or change provider-side limits.

Agent sessions launched by `agemux` use the official CLI dangerous permission bypass flags by default:

- Codex: `--dangerously-bypass-approvals-and-sandbox`
- Claude: `--dangerously-skip-permissions`

Use Agent Multiplexer this way only on trusted local machines or disposable sandboxes. To disable the bypass flags:

```sh
AGEMUX_CODEX_DANGEROUS=0 agemux codex
AGEMUX_CLAUDE_DANGEROUS=0 agemux claude
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
