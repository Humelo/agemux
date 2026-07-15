#!/usr/bin/env python3
import os
import pathlib
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]


def run(args, **kwargs):
    proc = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
    if proc.returncode != 0:
        raise SystemExit(f"{args!r} failed\nSTDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}")
    return proc


def cross_build_matrix(go):
    matrix = [
        ("linux", "amd64"),
        ("linux", "arm64"),
        ("darwin", "amd64"),
        ("darwin", "arm64"),
        ("windows", "amd64"),
        ("windows", "arm64"),
    ]
    for goos, goarch in matrix:
        suffix = ".exe" if goos == "windows" else ""
        env = dict(os.environ)
        env.update({"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch})
        with tempfile.TemporaryDirectory() as tmp:
            tmp = pathlib.Path(tmp)
            run([go, "build", "-trimpath", "-o", str(tmp / f"agemux{suffix}"), "./cmd/agemux"], cwd=ROOT, env=env)


def main():
    go = shutil.which("go") or "/home/linuxbrew/.linuxbrew/bin/go"
    if not pathlib.Path(go).exists() and shutil.which("go") is None:
        raise SystemExit("go not found")

    windows_main = (ROOT / "cmd" / "agemux" / "main_windows.go").read_text()
    if 'os.Args[1] == "codex-accounts"' not in windows_main:
        raise SystemExit("Windows entry point does not dispatch codex-accounts")

    run([go, "test", "./..."], cwd=ROOT)
    cross_build_matrix(go)

    with tempfile.TemporaryDirectory() as tmp:
        tmp = pathlib.Path(tmp)
        build = tmp / "build"
        build.mkdir()
        agemux_bin = build / ("agemux.exe" if os.name == "nt" else "agemux")
        run([go, "build", "-trimpath", "-o", str(agemux_bin), "./cmd/agemux"], cwd=ROOT)
        run([str(agemux_bin), "--help"])
        empty_codex_home = tmp / "empty-codex"
        empty_codex_home.mkdir()
        proc = run([str(agemux_bin), "codex-accounts"], env={**os.environ, "CODEX_HOME": str(empty_codex_home), "TERM": "dumb"})
        if "No Codex account files found" not in proc.stdout:
            raise SystemExit(f"unexpected codex-accounts empty output: {proc.stdout!r}")
        if os.name == "nt":
            proc = subprocess.run([str(agemux_bin), "list"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            if proc.returncode == 0 or "requires POSIX PTY" not in proc.stderr:
                raise SystemExit(f"Windows agemux non-help command should fail clearly: {proc.stdout!r} {proc.stderr!r}")
        proc = run([str(agemux_bin), "claude-accounts", "version"])
        if "Claude accounts 0.1.10" not in proc.stdout:
            raise SystemExit(f"unexpected Claude accounts version output: {proc.stdout!r}")

        home = tmp / "home"
        bin_dir = tmp / "bin"
        agemux_data = tmp / "agemux-data"
        home.mkdir()
        bin_dir.mkdir()
        if os.name != "nt":
            fake_shpool = bin_dir / "shpool"
            fake_shpool.write_text(
                '#!/usr/bin/env bash\n'
                'if [[ "$1 $2" == "list --json" ]]; then\n'
                '  printf \'{"sessions":[{"name":"agemux-20260706-010203-123-aaaa","status":"alive","started_at_unix_ms":1783296123000}]}\\n\'\n'
                '  exit 0\n'
                'fi\n'
                'echo "unexpected shpool args: $*" >&2\n'
                'exit 2\n'
            )
            fake_shpool.chmod(0o755)
            env_agemux = {
                "HOME": str(home),
                "PATH": f"{bin_dir}:{os.environ.get('PATH', '')}",
                "AGEMUX_SHPOOL_BIN": str(fake_shpool),
                "AGEMUX_DATA_DIR": str(agemux_data),
            }
            proc = run([str(agemux_bin), "list"], env=env_agemux)
            if "agemux-20260706-010203-123-aaaa" not in proc.stdout:
                raise SystemExit(f"agemux list did not include fake shpool session: {proc.stdout!r}")

        (home / ".claude").mkdir()
        (home / ".claude" / "settings.json").write_text("{}\n")
        agemux_path = bin_dir / ("agemux.exe" if os.name == "nt" else "agemux")
        shutil.copy2(agemux_bin, agemux_path)
        agemux_path.chmod(0o755)
        claude_path = bin_dir / ("claude.cmd" if os.name == "nt" else "claude")
        if os.name == "nt":
            claude_path.write_text('@echo off\r\necho config=%CLAUDE_CONFIG_DIR%\r\n')
        else:
            claude_path.write_text('#!/usr/bin/env bash\nprintf "config=%s\\n" "${CLAUDE_CONFIG_DIR:-}"\n')
        claude_path.chmod(0o755)
        env = {"HOME": str(home), "USERPROFILE": str(home), "PATH": f"{bin_dir}:{os.environ.get('PATH', '')}"}
        proc = subprocess.run([str(agemux_path), "claude-accounts", "change", "1"], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode != 0:
            raise SystemExit(f"agemux claude-accounts change failed: {proc.stderr}")
        proc = subprocess.run([str(agemux_path), "claude-accounts", "current"], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode != 0 or "current Claude account" not in proc.stdout:
            raise SystemExit(f"agemux claude-accounts current failed: {proc.stdout!r} {proc.stderr!r}")

        proc = subprocess.run([str(agemux_path), "claude-accounts", "install-shim", "--bin-dir", str(bin_dir)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode == 0:
            raise SystemExit("install-shim without --force unexpectedly replaced existing claude")
        proc = subprocess.run([str(agemux_path), "claude-accounts", "install-shim", "--force", "--bin-dir", str(bin_dir)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode != 0:
            raise SystemExit(f"install-shim --force failed: {proc.stderr}")
        proc = subprocess.run([str(claude_path)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        expected_config_dir = os.path.realpath(str(home / ".claude"))
        actual_config_dir = proc.stdout.strip().removeprefix("config=")
        if os.path.normcase(os.path.normpath(actual_config_dir)) != os.path.normcase(os.path.normpath(expected_config_dir)):
            raise SystemExit(f"claude shim did not inject current config: {proc.stdout!r} {proc.stderr!r}")
        proc = subprocess.run([str(agemux_path), "claude-accounts", "uninstall-shim", "--bin-dir", str(bin_dir)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode != 0:
            raise SystemExit(f"uninstall-shim failed: {proc.stderr}")
        proc = subprocess.run([str(agemux_path), "claude-accounts", "install-shim", "--force", "--bin-dir", str(bin_dir)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if proc.returncode != 0:
            raise SystemExit(f"install-shim after uninstall failed: {proc.stderr}")
        proc = subprocess.run([str(claude_path)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        actual_config_dir = proc.stdout.strip().removeprefix("config=")
        if os.path.normcase(os.path.normpath(actual_config_dir)) != os.path.normcase(os.path.normpath(expected_config_dir)):
            raise SystemExit(f"claude shim failed after reinstall: {proc.stdout!r} {proc.stderr!r}")

    print("smoke ok")


if __name__ == "__main__":
    main()
