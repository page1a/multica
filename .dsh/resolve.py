#!/usr/bin/env python3
"""Resolve git conflict markers in one file.

Usage: resolve.py <path> <ours|theirs|both|both-reverse> [hunk-index...]

- ours: keep the HEAD side of every conflict
- theirs: keep the upstream side
- both: ours then theirs (in that order)
- both-reverse: theirs then ours

With extra integer args, only those conflict hunks (0-based, in file order)
use the chosen strategy; the rest keep both sides. Useful when one file has a
mechanical conflict and one semantic one (empty string arg for the others).

Pass "-" for the strategy of "keep both sides" of remaining hunks.
"""
import sys
import re

MARKERS = re.compile(r"^(<<<<<<< |=======$|>>>>>>> )")


def parse(lines):
    """Split into segments: ('text', [lines]) and ('conflict', ours, theirs)."""
    segs = []
    buf = []
    i = 0
    while i < len(lines):
        line = lines[i]
        if line.startswith("<<<<<<< "):
            if buf:
                segs.append(("text", buf))
                buf = []
            ours, theirs = [], []
            i += 1
            while i < len(lines) and not lines[i].startswith("======="):
                ours.append(lines[i])
                i += 1
            i += 1  # skip =======
            while i < len(lines) and not lines[i].startswith(">>>>>>> "):
                theirs.append(lines[i])
                i += 1
            i += 1  # skip >>>>>>>
            segs.append(("conflict", ours, theirs))
        else:
            buf.append(line)
            i += 1
    if buf:
        segs.append(("text", buf))
    return segs


def main():
    path = sys.argv[1]
    strategy = sys.argv[2]
    only = set(int(a) for a in sys.argv[3:])
    with open(path, encoding="utf-8") as fh:
        lines = fh.read().split("\n")
    trailing_newline = lines and lines[-1] == ""
    if trailing_newline:
        lines = lines[:-1]

    segs = parse(lines)
    out = []
    index = 0
    n = 0
    for seg in segs:
        if seg[0] == "text":
            out.extend(seg[1])
            continue
        _, ours, theirs = seg
        if only and index not in only:
            out.extend(ours)
            out.extend(theirs)
        elif strategy == "ours":
            out.extend(ours)
        elif strategy == "theirs":
            out.extend(theirs)
        elif strategy == "both":
            out.extend(ours)
            out.extend(theirs)
        elif strategy == "both-reverse":
            out.extend(theirs)
            out.extend(ours)
        else:
            raise SystemExit(f"unknown strategy {strategy}")
        index += 1
        n += 1

    result = "\n".join(out)
    if trailing_newline:
        result += "\n"
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(result)
    print(f"{path}: resolved {n} conflict(s) -> {strategy}")


if __name__ == "__main__":
    main()
