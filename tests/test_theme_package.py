"""Guards on the Mono Blue package's discovery contract.

`osjs-cli package:discover` skips a local package silently when any of these
are wrong, so a rename or a missing marker would only show up as a theme that
stopped appearing in the desktop menu.
"""

import json
from pathlib import Path

import pytest

THEME = Path(__file__).resolve().parent.parent / "src" / "packages" / "MonoBlueTheme"


@pytest.fixture(scope="module")
def metadata():
    return json.loads((THEME / "metadata.json").read_text())


def test_the_package_carries_the_discovery_marker():
    package = json.loads((THEME / "package.json").read_text())
    assert package["osjs"]["type"] == "package"


def test_the_metadata_declares_a_named_theme(metadata):
    assert metadata["type"] == "theme"
    assert metadata["name"] == "MonoBlueTheme"
    assert metadata["title"]["en_EN"]


def test_every_declared_file_is_present(metadata):
    missing = [name for name in metadata["files"] if not (THEME / "dist" / name).is_file()]
    assert missing == []


def test_the_theme_ships_no_javascript(metadata):
    """CSS only, so it needs none of the window hooks a themed main.js provides."""
    assert metadata["files"] == ["main.css"]


def test_the_stylesheet_is_ascii():
    """The base stylesheet marks a checked toggle with an emoji; this replaces it."""
    css = (THEME / "dist" / "main.css").read_bytes()
    assert css.decode("ascii")


def test_the_palette_is_defined_once_at_the_root():
    css = (THEME / "dist" / "main.css").read_text()
    assert css.count(":root {") == 1
    for token in ("--mb-bg", "--mb-fg", "--mb-accent", "--mb-chrome-bg"):
        assert f"{token}:" in css
