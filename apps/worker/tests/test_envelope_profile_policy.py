"""UBAG_PROFILE_OPTIONS_POLICY: legacy (default) vs namespaced profile dirs."""
import os
import unittest

from ubag_worker.live.envelope import normalize_payload


def _payload(tenant, options=None):
    payload = {
        "api_version": "2026-05-22",
        "tenant_id": tenant,
        "job": {
            "target": "gemini_web",
            "command_type": "chat.prompt",
            "input": {"prompt": "hello"},
        },
    }
    if options:
        payload["job"]["options"] = dict(options)
    return payload


class ProfileOptionsPolicyTests(unittest.TestCase):
    def setUp(self):
        self._saved = {
            k: os.environ.get(k)
            for k in ("UBAG_PROFILE_DIR", "UBAG_PROFILE_OPTIONS_POLICY")
        }
        os.environ.pop("UBAG_PROFILE_DIR", None)
        os.environ.pop("UBAG_PROFILE_OPTIONS_POLICY", None)

    def tearDown(self):
        for key, value in self._saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    def test_default_is_legacy_and_honours_user_data_dir(self):
        job = normalize_payload(
            _payload("tenant_b", {"user_data_dir": "var/profiles/gemini_web/alice"}),
            "gemini_web",
        )
        self.assertEqual(job.user_data_dir, "var/profiles/gemini_web/alice")

    def test_unknown_policy_value_stays_legacy(self):
        os.environ["UBAG_PROFILE_OPTIONS_POLICY"] = "bogus"
        job = normalize_payload(
            _payload("tenant_b", {"profile_dir": "mine"}), "gemini_web"
        )
        self.assertEqual(job.user_data_dir, "mine")

    def test_namespaced_ignores_caller_profile_options(self):
        os.environ["UBAG_PROFILE_OPTIONS_POLICY"] = "namespaced"
        victim = os.path.join("var", "profiles", "tenant_a", "gemini_web", "default")
        for key in ("user_data_dir", "profile_dir", "profile_path"):
            job = normalize_payload(_payload("tenant_b", {key: victim}), "gemini_web")
            self.assertEqual(
                job.user_data_dir,
                os.path.join("var", "profiles", "tenant_b", "gemini_web", "default"),
                key,
            )

    def test_namespaced_separates_tenants_and_sanitises_ids(self):
        os.environ["UBAG_PROFILE_OPTIONS_POLICY"] = "namespaced"
        a = normalize_payload(_payload("tenant_a"), "gemini_web").user_data_dir
        b = normalize_payload(_payload("tenant_b"), "gemini_web").user_data_dir
        self.assertNotEqual(a, b)
        evil = normalize_payload(_payload("../../etc"), "gemini_web").user_data_dir
        self.assertNotIn("..", evil)
        self.assertTrue(evil.startswith(os.path.join("var", "profiles", "t_")))


if __name__ == "__main__":
    unittest.main()
