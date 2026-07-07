#!/usr/bin/env python3
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCAN_TARGETS = ["cmd", "internal", "legacy", "scripts", "packaging", ".github", "tests", "README.md", "RELEASE.md", "RELEASE_NOTES.md", "go.mod", "go.sum"]

FORBIDDEN_SUBSTRINGS = [
    "humelo" + ".com",
    "tools" + "-one",
    "billing" + "@",
    "tools" + "@",
    "tikita",
    "/home/" + "jrlee",
    "workspace/" + "tikita",
    "." + "claude" + "-sub",
    "OPENAI" + "_API_KEY",
    "ANTHROPIC" + "_API_KEY",
    "SUPA" + "BASE",
]

UUID_RE = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b", re.I)


def iter_files():
    for item in SCAN_TARGETS:
        path = ROOT / item
        if path.is_file():
            yield path
        elif path.is_dir():
            for child in path.rglob("*"):
                if child.is_file() and "__pycache__" not in child.parts:
                    yield child


def main():
    findings = []
    self_path = pathlib.Path(__file__).resolve()
    for path in iter_files():
        if path.resolve() == self_path:
            continue
        text = path.read_text(errors="ignore")
        lower = text.lower()
        for needle in FORBIDDEN_SUBSTRINGS:
            if needle.lower() in lower:
                findings.append(f"{path.relative_to(ROOT)} contains forbidden substring: {needle}")
        for match in UUID_RE.finditer(text):
            findings.append(f"{path.relative_to(ROOT)} contains UUID-like value: {match.group(0)}")
    if findings:
        print("\n".join(findings), file=sys.stderr)
        raise SystemExit(1)
    print("public safety ok")


if __name__ == "__main__":
    main()
