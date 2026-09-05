"""The suite must not import the implementation it is testing.

A conformance test that reaches into `server/` or `messaging/` stops being a
conformance test: it would pass against this server for reasons a second
implementation could not reproduce. `tui/` is excluded too, because it is the
other party to the contract rather than a description of it.

Checked by reading the suite's own source, which is the only way that stays
true for tests written after this one.
"""

import ast
from pathlib import Path

FORBIDDEN = {"server", "messaging", "tui"}

SUITE = Path(__file__).resolve().parent


def imported_roots(source):
    """Top-level package name of every import in one module."""
    roots = set()
    for node in ast.walk(ast.parse(source)):
        if isinstance(node, ast.Import):
            roots.update(alias.name.split(".")[0] for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
            roots.add(node.module.split(".")[0])
    return roots


def test_no_module_imports_the_implementation():
    offenders = {}
    for path in sorted(SUITE.glob("*.py")):
        found = imported_roots(path.read_text()) & FORBIDDEN
        if found:
            offenders[path.name] = sorted(found)

    assert offenders == {}, f"conformance tests must be black box: {offenders}"


def test_the_suite_was_actually_read():
    """Guards the guard: a glob that matched nothing would pass silently."""
    assert len(list(SUITE.glob("test_*.py"))) >= 2
