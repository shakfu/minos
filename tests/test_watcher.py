import pytest


@pytest.fixture
def watcher(app, tmp_path):
    from server import sockets

    registry = sockets.Registry()
    return sockets.DistWatcher(registry, tmp_path / "dist"), registry


class Recorder:
    def __init__(self):
        self.frames = []

    def send(self, frame):
        self.frames.append(frame)


def subscribe(registry):
    from server import sockets

    recorder = Recorder()
    registry.add(sockets.Connection(recorder, {"username": "demo"}))
    return recorder


def test_a_quiet_directory_broadcasts_nothing(watcher):
    instance, registry = watcher
    subscribe(registry)

    assert instance.poll() == []


def test_a_rebuilt_stylesheet_signals_a_hot_reload(watcher, tmp_path):
    instance, registry = watcher
    recorder = subscribe(registry)

    (tmp_path / "dist" / "osjs.css").write_text("body{}")

    assert instance.poll() == ["osjs.css"]
    assert '"name": "osjs/dist:changed"' in recorder.frames[0]
    assert '"/osjs.css"' in recorder.frames[0]


def test_a_changed_manifest_signals_a_package_reload(watcher, tmp_path):
    instance, registry = watcher
    recorder = subscribe(registry)

    (tmp_path / "dist" / "metadata.json").write_text('[{"name": "Textpad"}]')

    assert instance.poll() == ["metadata.json"]
    assert '"name": "osjs/packages:metadata:changed"' in recorder.frames[0]


def test_unchanged_files_are_not_re_announced(watcher, tmp_path):
    instance, registry = watcher
    (tmp_path / "dist" / "osjs.js").write_text("boot()")
    instance.poll()

    assert instance.poll() == []


def test_a_same_size_rewrite_is_still_detected(watcher, tmp_path):
    instance, registry = watcher
    target = tmp_path / "dist" / "osjs.js"
    target.write_text("aaa")
    instance.poll()

    target.write_text("bbb")

    assert instance.poll() == ["osjs.js"]


def test_other_file_types_are_ignored(watcher, tmp_path):
    instance, registry = watcher
    (tmp_path / "dist" / "index.html").write_text("<html></html>")
    (tmp_path / "dist" / "favicon.png").write_bytes(b"\x89PNG")

    assert instance.poll() == []


def test_a_missing_directory_does_not_raise(app, tmp_path):
    from server import sockets

    instance = sockets.DistWatcher(sockets.Registry(), tmp_path / "absent")

    assert instance.poll() == []
