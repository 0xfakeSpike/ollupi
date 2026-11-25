"""The verifier of a probe task: checks the agent's answers and files against /tests/expect.json, and writes the reward."""

import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ITEMS = Path("/logs/agent/ollupi-items.json")
VERIFIER = Path("/logs/verifier")


def answers(items):
    last = {}
    for it in items:
        if it["type"] == "agentMessage" and it.get("text"):
            last[it.get("turn", 0)] = it["text"]
    return [last[t] for t in sorted(last)]


def folded(s):
    return re.sub(r"\s+", "", s).lower()


def numbers(text):
    found = []
    for m in re.finditer(r"(\d[\d,]*(?:\.\d+)?)\s*(万|亿)?", text):
        value = float(m.group(1).replace(",", ""))
        found.append(value * {"万": 1e4, "亿": 1e8}.get(m.group(2), 1))
    return found


def run(check, said):
    kind = check["kind"]
    if kind == "any":
        text = folded(said[check.get("turn", -1)])
        return any(folded(n) in text for n in check["of"]), "none of %s" % check["of"]
    if kind == "number":
        want, tol = check["value"], check.get("tolerance", 0.002)
        got = numbers(said[check.get("turn", -1)])
        return any(abs(n - want) <= abs(want) * tol for n in got), "no number within %g%% of %s" % (tol * 100, want)
    if kind == "order":
        text = said[check.get("turn", -1)]
        at = [text.find(n) for n in check["of"]]
        return -1 not in at and at == sorted(at), "not in the order %s" % check["of"]
    if kind == "lines":
        path = Path(check["path"])
        if not path.is_file():
            return False, "%s is missing" % path
        lines = [l for l in path.read_text().splitlines() if re.match(check["match"], l)]
        keys = [re.match(check["match"], l).group(1) for l in lines]
        if keys != check["keys"]:
            return False, "%d matching lines, keys %s, want %s" % (len(lines), keys[:5], check["keys"][:5])
        text = folded(path.read_text())
        missing = [n for n in check.get("contains", []) if folded(n) not in text]
        return not missing, "missing %s" % missing
    if kind == "sha256":
        got = hashlib.sha256(Path(check["path"]).read_bytes()).hexdigest()
        return got == check["value"], "%s was changed" % check["path"]
    if kind == "command":
        r = subprocess.run(check["argv"], cwd=check.get("cwd"), capture_output=True, text=True)
        return r.returncode == 0, "exit %d: %s" % (r.returncode, (r.stdout + r.stderr)[-500:])
    raise ValueError("unknown check %s" % kind)


def main():
    expect = json.loads(Path("/tests/expect.json").read_text())
    data = json.loads(ITEMS.read_text()) if ITEMS.is_file() else {"items": []}
    said = answers(data["items"])
    results = []
    for i, check in enumerate(expect["checks"]):
        name = "%s[%d] %s" % (expect["name"], i, check["kind"])
        try:
            ok, why = run(check, said)
        except IndexError:
            ok, why = False, "no answer to that message"
        except Exception as e:
            ok, why = False, repr(e)
        print(("PASSED " if ok else "FAILED ") + name + ("" if ok else ": " + why))
        results.append({"name": name, "status": "passed" if ok else "failed"})
    passed = sum(r["status"] == "passed" for r in results)
    print("=== %d passed, %d failed ===" % (passed, len(results) - passed))
    VERIFIER.mkdir(parents=True, exist_ok=True)
    (VERIFIER / "ctrf.json").write_text(json.dumps({"results": {"tool": {"name": "probe"}, "tests": results}}))
    (VERIFIER / "reward.txt").write_text("1\n" if passed == len(results) else "0\n")


if __name__ == "__main__":
    sys.exit(main())
