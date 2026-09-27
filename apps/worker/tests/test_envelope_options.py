"""Envelope option validation: profile-path containment and headless typing.

``normalize_payload`` is the trust boundary between the gateway dispatch
envelope and the browser. An explicit ``user_data_dir`` / ``profile_dir`` /
``profile_path`` is handed to Chromium as the persistent-context directory,
so it must stay inside the worker's own profile state (``UBAG_PROFILE_DIR``,
default ``var/profiles``) and never traverse out with ``..``. ``headless``
must be a real boolean: ``bool("false")`` is True, so a string value would
silently launch a headed browser.
"""
import os
import unittest

from ubag_worker.live.envelope import EnvelopeError, normalize_payload


def _payload(options=None, **top_level):
    payload = {
        "api_version": "2026-05-22",
        "job": {
            "target": "gemini_web",
            "command_type": "chat.prompt",
            "input": {"prompt": "hello"},
        },
    }
    if options:
        payload["job"]["options"] = dict(options)
    payload.update(top_level)
    return payload


def _absolute_root(name):
    # An absolute base on the current drive; nothing is created on disk, the
    # path only needs to be absolute and stable for containment checks.
    return os.path.join(os.path.abspath(os.sep), name)


class ProfilePathContainmentTests(unittest.TestCase):
    def setUp(self):
        self._saved = os.environ.get("UBAG_PROFILE_DIR")
        os.environ.pop("UBAG_PROFILE_DIR", None)

    def tearDown(self):
        if self._saved is None:
            os.environ.pop("UBAG_PROFILE_DIR", None)
        else:
            os.environ["UBAG_PROFILE_DIR"] = self._saved

    def test_relative_profile_path_is_allowed(self):
        job = normalize_payload(
            _payload({"user_data_dir": "var/profiles/gemini_web/alice"}), "gemini_web"
        )
        self.assertEqual(job.user_data_dir, "var/profiles/gemini_web/alice")

    def test_absolute_path_inside_profile_root_is_allowed(self):
        root = _absolute_root("ubag-test-profiles")
        os.environ["UBAG_PROFILE_DIR"] = root
        inside = os.path.join(root, "gemini_web", "alice")
        job = normalize_payload(_payload({"user_data_dir": inside}), "gemini_web")
        self.assertEqual(job.user_data_dir, inside)

    def test_absolute_path_outside_profile_root_is_rejected(self):
        os.environ["UBAG_PROFILE_DIR"] = _absolute_root("ubag-test-profiles")
        outside = os.path.join(
            _absolute_root("elsewhere"), "profiles", "gemini_web", "alice"
        )
        with self.assertRaises(EnvelopeError):
            normalize_payload(_payload({"user_data_dir": outside}), "gemini_web")

    def test_absolute_path_rejected_when_no_profile_root_configured(self):
        outside = os.path.join(_absolute_root("home"), "user", "profiles", "main")
        with self.assertRaises(EnvelopeError):
            normalize_payload(_payload({"user_data_dir": outside}), "gemini_web")

    def test_parent_traversal_is_rejected(self):
        with self.assertRaises(EnvelopeError):
            normalize_payload(
                _payload({"user_data_dir": "var/profiles/../../etc"}), "gemini_web"
            )

    def test_windows_style_traversal_is_rejected(self):
        with self.assertRaises(EnvelopeError):
            normalize_payload(
                _payload({"profile_dir": "profiles\\..\\..\\windows"}), "gemini_web"
            )

    def test_profile_path_and_profile_dir_keys_are_validated(self):
        os.environ["UBAG_PROFILE_DIR"] = _absolute_root("ubag-test-profiles")
        outside = os.path.join(_absolute_root("elsewhere"), "profile")
        for key in ("profile_dir", "profile_path"):
            with self.assertRaises(EnvelopeError):
                normalize_payload(_payload({key: outside}), "gemini_web")

    def test_manual_context_profile_path_is_validated(self):
        with self.assertRaises(EnvelopeError):
            normalize_payload(
                _payload(user_data_dir="var/profiles/../../escape"), "gemini_web"
            )

    def test_default_resolution_unchanged_without_env(self):
        job = normalize_payload(_payload(), "gemini_web")
        self.assertEqual(job.user_data_dir, os.path.join("var", "profiles", "gemini_web", "default"))

    def test_default_resolution_uses_env_root(self):
        root = _absolute_root("ubag-test-profiles")
        os.environ["UBAG_PROFILE_DIR"] = root
        job = normalize_payload(_payload(), "gemini_web")
        self.assertEqual(job.user_data_dir, os.path.join(root, "gemini_web"))


class HeadlessTypingTests(unittest.TestCase):
    def test_headless_bool_true_and_false_pass_through(self):
        self.assertTrue(normalize_payload(_payload({"headless": True}), "gemini_web").headless)
        self.assertFalse(normalize_payload(_payload({"headless": False}), "gemini_web").headless)

    def test_headless_defaults_to_false(self):
        self.assertFalse(normalize_payload(_payload(), "gemini_web").headless)

    def test_headless_string_is_rejected(self):
        with self.assertRaises(EnvelopeError):
            normalize_payload(_payload({"headless": "false"}), "gemini_web")

    def test_headless_int_is_rejected(self):
        with self.assertRaises(EnvelopeError):
            normalize_payload(_payload({"headless": 1}), "gemini_web")


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
