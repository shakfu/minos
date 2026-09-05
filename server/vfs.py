"""Virtual filesystem backing the OS.js /vfs/* API.

Paths arrive from the client as "<mountpoint>:/<path>". Each mountpoint maps to
a real directory; every resolved path is checked against its mountpoint root so
a crafted path cannot escape it.
"""

import fnmatch
import mimetypes
import shutil
from datetime import datetime, timezone
from pathlib import Path

from . import config

for suffix, mime in config.EXTRA_MIME_TYPES.items():
    mimetypes.add_type(mime, suffix)


class VfsError(Exception):
    """A VFS request that must be reported to the client with a status code."""

    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


def home_root(username):
    return config.VFS_ROOT / username


MOUNTPOINTS = {
    "osjs": {"root": lambda username: config.DIST, "read_only": True},
    "home": {"root": home_root, "read_only": False},
}

READ_ONLY_METHODS = {"capabilities", "exists", "stat", "readdir", "readfile", "search"}


def guess_mime(path):
    if path.name in config.MIME_BY_FILENAME:
        return config.MIME_BY_FILENAME[path.name]
    return mimetypes.guess_type(path.name)[0] or "application/octet-stream"


def resolve(virtual_path, username, method):
    """Map a virtual path to a real one, refusing escapes and read-only writes."""
    if not isinstance(virtual_path, str) or ":" not in virtual_path:
        raise VfsError(f"Malformed VFS path: {virtual_path!r}")

    name, _, rest = virtual_path.partition(":")
    mount = MOUNTPOINTS.get(name)
    if mount is None:
        raise VfsError(f"No such mountpoint: {name}", 404)

    if mount["read_only"] and method not in READ_ONLY_METHODS:
        raise VfsError(f"Mountpoint {name} is read-only", 403)

    root = mount["root"](username).resolve()
    target = (root / rest.lstrip("/")).resolve()
    if target != root and root not in target.parents:
        raise VfsError("Path escapes its mountpoint", 403)

    return target


def _virtual_parent(virtual_path):
    name, _, rest = virtual_path.partition(":")
    return f"{name}:{rest.rstrip('/')}"


def _iso(seconds):
    return datetime.fromtimestamp(seconds, tz=timezone.utc).isoformat()


def file_iter(virtual_path, target):
    """Build the file descriptor object the client expects from readdir/stat."""
    stat = target.stat()
    is_dir = target.is_dir()

    return {
        "isDirectory": is_dir,
        "isFile": target.is_file(),
        "mime": None if is_dir else guess_mime(target),
        "size": stat.st_size,
        "path": virtual_path,
        "filename": target.name,
        "stat": {
            "size": stat.st_size,
            "mode": stat.st_mode,
            "atime": _iso(stat.st_atime),
            "mtime": _iso(stat.st_mtime),
            "ctime": _iso(stat.st_ctime),
            "atimeMs": stat.st_atime * 1000,
            "mtimeMs": stat.st_mtime * 1000,
            "ctimeMs": stat.st_ctime * 1000,
        },
    }


def capabilities(username, path, options=None):
    resolve(path, username, "capabilities")
    return {"sort": False, "pagination": False}


def exists(username, path, options=None):
    return resolve(path, username, "exists").exists()


def stat(username, path, options=None):
    target = resolve(path, username, "stat")
    if not target.exists():
        raise VfsError(f"No such file: {path}", 404)
    return file_iter(path, target)


def readdir(username, path, options=None):
    target = resolve(path, username, "readdir")
    if not target.is_dir():
        raise VfsError(f"Not a directory: {path}", 404)

    base = _virtual_parent(path)
    entries = []
    for child in sorted(target.iterdir(), key=lambda p: p.name.lower()):
        try:
            entries.append(file_iter(f"{base}/{child.name}", child))
        except OSError:
            continue
    return entries


def readfile(username, path, options=None):
    target = resolve(path, username, "readfile")
    if not target.is_file():
        raise VfsError(f"No such file: {path}", 404)
    return target


def writefile(username, path, stream):
    target = resolve(path, username, "writefile")
    target.parent.mkdir(parents=True, exist_ok=True)

    written = 0
    with open(target, "wb") as handle:
        while True:
            chunk = stream.read(64 * 1024)
            if not chunk:
                break
            handle.write(chunk)
            written += len(chunk)
    return written


def mkdir(username, path, options=None):
    target = resolve(path, username, "mkdir")
    ensure = bool((options or {}).get("ensure"))
    if target.exists():
        if ensure:
            return True
        raise VfsError(f"Already exists: {path}", 409)
    target.mkdir(parents=ensure)
    return True


def unlink(username, path, options=None):
    target = resolve(path, username, "unlink")
    if not target.exists():
        raise VfsError(f"No such file: {path}", 404)
    if target.is_dir():
        shutil.rmtree(target)
    else:
        target.unlink()
    return True


def touch(username, path, options=None):
    target = resolve(path, username, "touch")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.touch()
    return True


def copy(username, source, destination, options=None):
    src = resolve(source, username, "readfile")
    dest = resolve(destination, username, "writefile")
    if not src.exists():
        raise VfsError(f"No such file: {source}", 404)
    if src.is_dir():
        shutil.copytree(src, dest)
    else:
        shutil.copy2(src, dest)
    return True


def rename(username, source, destination, options=None):
    src = resolve(source, username, "unlink")
    dest = resolve(destination, username, "writefile")
    if not src.exists():
        raise VfsError(f"No such file: {source}", 404)
    shutil.move(str(src), str(dest))
    return True


def search(username, root, pattern, options=None):
    target = resolve(root, username, "search")
    if not target.is_dir():
        raise VfsError(f"Not a directory: {root}", 404)

    base = _virtual_parent(root)
    glob = pattern if any(c in pattern for c in "*?[") else f"*{pattern}*"

    # Match first, then sort, then stat. Sorting the whole walk before looking
    # at it meant every path in the tree was ordered to find a hundred, and
    # `sorted()` consumed the generator whole; matching is a name comparison
    # with no syscall behind it. The order and the resulting slice are the same.
    matches = sorted(
        child for child in target.rglob("*")
        if fnmatch.fnmatch(child.name.lower(), glob.lower())
    )

    results = []
    for child in matches:
        relative = child.relative_to(target).as_posix()
        try:
            results.append(file_iter(f"{base}/{relative}", child))
        except OSError:
            continue
        if len(results) >= config.SEARCH_LIMIT:
            break
    return results


def ensure_home(username):
    """Create the user's home directory and seed it on first login."""
    root = home_root(username)
    root.mkdir(parents=True, exist_ok=True)
    for relative, contents in config.HOME_TEMPLATE.items():
        target = root / relative
        if not target.exists():
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(contents)
    return root
