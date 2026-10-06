"""UBAG_HELPER_PLANE: a helper-node worker derives its profile dir from profile_ref only."""
import os
import unittest

from ubag_worker.live.envelope import EnvelopeError, normalize_payload

REF = "pr_6H3QWJ2ZB5M4XK7RD2N5T3YAFE"
ENV_KEYS = ("UBAG_PROFILE_DIR", "UBAG_PROFILE_OPTIONS_POLICY", "UBAG_HELPER_PLANE")


def _payload(tenant="tenant_b", profile_ref=REF, options=None, context=None, target="gemini_web"):
    """The shape of the primary's HelperAttemptSpec (flat, profile_ref on top)."""
    payload = {
        "api_version": "2026-05-22",
        "tenant_id": tenant,
        "target": target,
        "input": {"prompt": "hello"},
    }
    if profile_ref is not None:
        payload["profile_ref"] = profile_ref
    if options:
        payload["options"] = dict(options)
    if context:
        payload["context"] = dict(context)
    return payload


class HelperProfileTests(unittest.TestCase):
    def setUp(self):
        self._saved = {k: os.environ.get(k) for k in ENV_KEYS}
        for key in ENV_KEYS:
            os.environ.pop(key, None)

    def tearDown(self):
        for key, value in self._saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    def _helper(self, on=True):
        if on:
            os.environ["UBAG_HELPER_PLANE"] = "true"

    def _dir(self, payload, target="gemini_web"):
        return normalize_payload(payload, target).user_data_dir

    def test_flag_off_ignores_profile_ref_and_keeps_legacy_behaviour(self):
        job = normalize_payload(
            _payload(options={"user_data_dir": "var/profiles/gemini_web/alice"}), "gemini_web"
        )
        self.assertEqual(job.user_data_dir, "var/profiles/gemini_web/alice")
        self.assertEqual(job.account_binding_id, "unbound")
        # Absent ref is fine too when the flag is off.
        self.assertEqual(
            self._dir(_payload(profile_ref=None)), os.path.join("var", "profiles", "gemini_web", "default")
        )

    def test_helper_mode_derives_dir_from_profile_ref_only(self):
        self._helper()
        expected = os.path.join("var", "profiles", "helper", REF)
        victim = os.path.join("var", "profiles", "tenant_a", "gemini_web", "default")
        for key in ("user_data_dir", "profile_dir", "profile_path"):
            self.assertEqual(self._dir(_payload(options={key: victim})), expected, key)
            self.assertEqual(self._dir(_payload(context={key: victim})), expected, key)
        # Tenant, target and caller identity do not move the directory.
        self.assertEqual(self._dir(_payload(tenant="someone_else")), expected)
        self.assertEqual(self._dir(_payload(target="chatgpt_web"), "chatgpt_web"), expected)
        self.assertEqual(
            self._dir(_payload(context={"account_binding_id": "acct_of_tenant_a"})), expected
        )

    def test_helper_mode_beats_the_namespaced_policy_and_honours_profile_root(self):
        self._helper()
        os.environ["UBAG_PROFILE_OPTIONS_POLICY"] = "namespaced"
        os.environ["UBAG_PROFILE_DIR"] = os.path.join("state", "profiles")
        self.assertEqual(self._dir(_payload()), os.path.join("state", "profiles", "helper", REF))

    def test_distinct_refs_get_distinct_dirs(self):
        self._helper()
        other = "pr_2222222222222222222222222A"
        self.assertNotEqual(self._dir(_payload()), self._dir(_payload(profile_ref=other)))

    def test_account_binding_id_is_the_profile_ref(self):
        self._helper()
        job = normalize_payload(
            _payload(context={"account_binding_id": "acct_of_tenant_a"}), "gemini_web"
        )
        self.assertEqual(job.account_binding_id, REF)

    def test_helper_mode_requires_a_valid_top_level_profile_ref(self):
        self._helper()
        bad = [None, "", " ", "..", "../x", "a/b", "a\\b", "a:b", ".hidden", "-lead", "x" * 129, 7, ["a"]]
        for value in bad:
            payload = _payload(profile_ref=None)
            if value is not None:
                payload["profile_ref"] = value
            with self.assertRaises(EnvelopeError, msg=repr(value)):
                normalize_payload(payload, "gemini_web")

    def test_profile_ref_is_not_read_from_caller_controlled_places(self):
        self._helper()
        for where in ("options", "context", "job", "input"):
            payload = _payload(profile_ref=None)
            nested = dict(payload.get(where, {})) if where != "job" else {}
            nested["profile_ref"] = REF
            payload[where] = nested
            with self.assertRaises(EnvelopeError, msg=where):
                normalize_payload(payload, "gemini_web")

    def test_accepts_dots_dashes_and_the_longest_ref(self):
        self._helper()
        for ref in ("a", "A1._-", "p" * 128):
            self.assertEqual(
                self._dir(_payload(profile_ref=ref)), os.path.join("var", "profiles", "helper", ref)
            )


if __name__ == "__main__":
    unittest.main()
