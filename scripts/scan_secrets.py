#!/usr/bin/env python3
"""Conservative release scan. Reports locations/categories, never matched values."""
import argparse
from pathlib import Path
import re
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--mode", choices=["files", "staged", "head"], default="files")
args = parser.parse_args()
patterns = {
    "API key signature": re.compile(r"sk-[A-Za-z0-9_-]{20,}"),
    "GitHub token signature": re.compile(r"(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})"),
    "private SSH/PEM key": re.compile(r"-----BEGIN (?:OPENSSH |RSA |EC )?PRIVATE KEY-----"),
    "personal machine path": re.compile(r"/home/[^/ ]+/(?:go-learning|projects)|DESKTOP-[A-Z0-9]{5,}|C:[/\\]Users[/\\][A-Za-z0-9_-]+"),
}
secret_assignment = re.compile(r"^\s*(?:export\s+)?(?:DEEPSEEK_API_KEY|GH_TOKEN|GITHUB_TOKEN)\s*=\s*[\"']?([^\s\"']+)", re.M)
password_url = re.compile(r"postgres(?:ql)?://[^:\s/]+:([^@\s/]+)@")
excluded = {".git", "work", "worktrees", "bin", "dist", "build", "__pycache__"}
if args.mode == "staged":
    raw = subprocess.check_output(["git", "diff", "--cached", "--name-only", "--diff-filter=ACM", "-z"], cwd=root)
    names = raw.decode().split("\0")
elif args.mode == "head":
    names = subprocess.check_output(["git", "ls-tree", "-rz", "--name-only", "HEAD"], cwd=root).decode().split("\0")
elif (root / ".git").exists():
    names = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=root).decode().split("\0")
else:
    names = [str(p.relative_to(root)) for p in root.rglob("*") if p.is_file() and not set(p.relative_to(root).parts) & excluded]
findings = []
files = 0
for name in sorted(set(filter(None, names))):
    p = root / name
    if args.mode == "staged":
        data = subprocess.check_output(["git", "show", ":" + name], cwd=root)
    elif args.mode == "head":
        data = subprocess.check_output(["git", "show", "HEAD:" + name], cwd=root)
    else:
        data = p.read_bytes()
    files += 1
    if p.name.startswith(".env") and p.name != ".env.example":
        findings.append((name, 0, "local environment file"))
    if data.startswith(b"\x7fELF") or data.startswith(b"\x00"):
        findings.append((name, 0, "binary artifact"))
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        findings.append((name, 0, "non-text release file"))
        continue
    for line_no, line in enumerate(text.splitlines(), 1):
        for category, pattern in patterns.items():
            # The scanner's own pattern describes a machine-path rule, not a machine file.
            if category == "personal machine path" and name == "scripts/scan_secrets.py":
                continue
            if pattern.search(line):
                findings.append((name, line_no, category))
        match = secret_assignment.search(line)
        if match and not match.group(1).startswith(("$", "YOUR_", "EXAMPLE_")):
            findings.append((name, line_no, "literal secret assignment"))
        match = password_url.search(line)
        if match:
            value = match.group(1)
            placeholder = value in {"PASSWORD", "TEST_PASSWORD", "ci-only-password"}
            if value == "ci-only-password" and name != ".github/workflows/ci.yml":
                placeholder = False
            if not placeholder:
                findings.append((name, line_no, "database URL with literal password"))
print(f"Scanned {files} {args.mode} files; findings={len(findings)}")
for name, line_no, category in findings:
    print(f"{name}:{line_no}: {category} [REDACTED]")
if findings:
    sys.exit(1)
