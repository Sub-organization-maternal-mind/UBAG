"""Path-traversal / arbitrary-file-upload gates on payload-supplied local paths.

``input.attachment_local_paths`` and ``input.audio_local_path`` are handed to
Playwright's ``set_input_files``, which UPLOADS the file's bytes to a
third-party AI provider. The declaration KEY is validated elsewhere, but these
paths used to be accepted verbatim, so a job payload could name any readable
file on the host - an SSH private key, /etc/passwd, a mounted secret - and have
its contents shipped to the model vendor.

Safety depended entirely on the Go runner overwriting the field before dispatch.
These tests pin the worker's own fail-closed check, which is what makes that
overwrite defence in depth rather than the only thing standing there.
"""

import os
import tempfile

import pytest
from ubag_worker.live.envelope import EnvelopeError, normalize_payload

PROVIDER = "chatgpt_web"


def _payload(**input_overrides):
    job_input = {"prompt": "attach this"}
    job_input.update(input_overrides)
    return {
        "job_id": "job-attach-path-guard",
        "api_version": "2026-05-22",
        "idempotency_key": "attach-path-guard",
        "job": {
            "target": PROVIDER,
            "command_type": "chat.completions",
            "input": job_input,
        },
    }


def _doc(key="report", content_type="application/pdf", kind="document"):
    return {"key": key, "content_type": content_type, "kind": kind}


def _materialized(name="report.pdf"):
    """A path shaped exactly like the gateway's materializeAttachments output."""
    tmp = tempfile.mkdtemp(prefix="ubag-attach-")
    path = os.path.join(tmp, name)
    with open(path, "wb") as handle:
        handle.write(b"%PDF-1.4 test")
    return path


def _normalize(**input_overrides):
    return normalize_payload(_payload(**input_overrides), PROVIDER)


class TestAttachmentLocalPaths:
    def test_gateway_materialized_path_is_accepted(self):
        path = _materialized()
        job = _normalize(attachments=[_doc()], attachment_local_paths=[path])
        assert job.attachment_local_paths == (path,)

    @pytest.mark.parametrize(
        "target",
        [
            "/etc/passwd",
            "/root/.ssh/id_rsa",
            "/var/run/secrets/kubernetes.io/serviceaccount/token",
            r"C:\Windows\System32\config\SAM",
        ],
    )
    def test_absolute_paths_outside_temp_are_rejected(self, target):
        with pytest.raises(EnvelopeError) as excinfo:
            _normalize(attachments=[_doc()], attachment_local_paths=[target])
        assert "attachment_local_paths" in str(excinfo.value)

    def test_relative_path_is_rejected(self):
        with pytest.raises(EnvelopeError):
            _normalize(attachments=[_doc()], attachment_local_paths=["../../etc/passwd"])

    def test_any_temp_subdirectory_is_accepted(self):
        """The bound is the temp ROOT, not one subdirectory name.

        The gateway's temp folder is an implementation detail of
        executor/workerconsumer.go; pinning its name would reject legitimate
        attachments the moment that changed, so only containment in the system
        temp directory is enforced.
        """
        other = tempfile.mkdtemp(prefix="somebody-else-")
        path = os.path.join(other, "doc.txt")
        with open(path, "w", encoding="utf-8") as handle:
            handle.write("doc")
        job = _normalize(attachments=[_doc("r", "text/plain")], attachment_local_paths=[path])
        assert job.attachment_local_paths == (path,)

    def test_symlink_escape_is_rejected(self):
        """A symlink inside an allowed folder must not launder an outside target."""
        allowed = tempfile.mkdtemp(prefix="ubag-attach-")
        link = os.path.join(allowed, "escape.txt")
        try:
            os.symlink("/etc/passwd", link)
        except (OSError, NotImplementedError):  # pragma: no cover - Windows perms
            pytest.skip("symlink creation not permitted on this platform")
        with pytest.raises(EnvelopeError):
            _normalize(attachments=[_doc("r", "text/plain")], attachment_local_paths=[link])

    def test_count_mismatch_still_enforced(self):
        path = _materialized()
        with pytest.raises(EnvelopeError) as excinfo:
            _normalize(
                attachments=[_doc("a", "text/plain"), _doc("b", "text/plain")],
                attachment_local_paths=[path],
            )
        assert "same number of entries" in str(excinfo.value)


class TestAudioLocalPath:
    def test_gateway_materialized_path_is_accepted(self):
        path = _materialized("voice.wav")
        job = _normalize(audio_artifact_key="voice", audio_local_path=path)
        assert job.audio_local_path == path

    def test_arbitrary_path_is_rejected(self):
        with pytest.raises(EnvelopeError) as excinfo:
            _normalize(audio_artifact_key="voice", audio_local_path="/etc/shadow")
        assert "audio_local_path" in str(excinfo.value)


class TestTextOnlyJobsUnaffected:
    def test_no_attachments_still_parses(self):
        job = _normalize()
        assert job.attachment_local_paths == ()
        assert not job.audio_local_path


class TestThreadRefSameOrigin:
    """thread_ref is navigated in the AUTHENTICATED browser and its content is
    read back as the job result, so it must stay on the provider's own origin."""

    def test_same_origin_https_accepted(self):
        from ubag_worker.live.page_driver import _is_same_origin_https

        assert _is_same_origin_https(
            "https://chatgpt.com/c/abc-123", "https://chatgpt.com/"
        ) is True
        # Default-port spellings are the same origin.
        assert _is_same_origin_https(
            "https://chatgpt.com:443/c/abc", "https://chatgpt.com"
        ) is True

    @pytest.mark.parametrize(
        "candidate",
        [
            "http://chatgpt.com/c/abc",                       # plaintext
            "https://evil.example/c/abc",                      # foreign origin
            "https://chatgpt.com.evil.example/c/abc",          # suffix confusion
            "https://sub.chatgpt.com/c/abc",                   # different host
            "https://127.0.0.1:9222/json",                     # loopback / CDP
            "https://169.254.169.254/latest/meta-data/",       # cloud metadata
            "https://10.0.0.5/internal",                       # RFC1918
            "file:///etc/passwd",                              # local file
            "javascript:alert(1)",                             # script scheme
            "data:text/html,<script>1</script>",              # data scheme
            "//chatgpt.com/c/abc",                             # scheme-relative
            "not a url at all",
            "",
        ],
    )
    def test_rejected(self, candidate):
        from ubag_worker.live.page_driver import _is_same_origin_https

        assert _is_same_origin_https(candidate, "https://chatgpt.com/") is False

    def test_deepseek_and_gemini_origins(self):
        from ubag_worker.live.page_driver import _is_same_origin_https

        assert _is_same_origin_https(
            "https://chat.deepseek.com/a/chat/s/1", "https://chat.deepseek.com/"
        ) is True
        assert _is_same_origin_https(
            "https://chat.deepseek.com/a/chat/s/1", "https://gemini.google.com/app"
        ) is False
