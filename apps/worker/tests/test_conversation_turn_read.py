"""Multi-turn conversation read correctness for the live ``PlaywrightPageDriver``.

Regression test for the conversation-recall bug. On a RESUMED thread the page
already shows the prior assistant turns, but the response reader bound
``locator(sel).first`` -- the OLDEST turn. So the second turn in a conversation
returned the FIRST turn's answer (asking "what was the word?" replied with the
prior turn's "OK" instead of the newly generated "BANANA"). Single-turn jobs
looked fine only because a fresh chat has exactly one assistant node, where
``.first`` and ``.last`` are the same element.

The reader must target the turn that was just submitted -- the NEWEST assistant
node -- and must first wait for that new node to appear (a resumed thread's prior
answer is already rendered and stable, so a naive read settles instantly on the
wrong text).

These tests drive the real ``PlaywrightPageDriver`` DOM logic through a fake
Playwright page that models a chronological transcript; no browser is launched.
"""
import pytest

from ubag_worker.live.page_driver import PlaywrightPageDriver
from ubag_worker.live.selectors import CHATGPT_WEB, DEEPSEEK_WEB


class _Element:
    """A single resolved node -- Playwright's locator after ``.first``/``.last``."""

    def __init__(self, *, text=None, on_action=None, visible=True):
        self._text = text
        self._on_action = on_action
        self._visible = visible

    def wait_for(self, **_kwargs):
        if not self._visible:
            raise RuntimeError("selector matched nothing")

    def inner_text(self, timeout=None):
        return self._text or ""

    def click(self, **_kwargs):
        if self._on_action is not None:
            self._on_action()

    def fill(self, _text, **_kwargs):
        pass

    def press(self, _key, **_kwargs):
        if self._on_action is not None:
            self._on_action()


class _ListLocator:
    """A locator matching a chronological list of text nodes (0..N of them)."""

    def __init__(self, texts):
        self._texts = list(texts)

    def count(self):
        return len(self._texts)

    @property
    def first(self):
        return _Element(text=self._texts[0], visible=True) if self._texts else _Element(visible=False)

    @property
    def last(self):
        return _Element(text=self._texts[-1], visible=True) if self._texts else _Element(visible=False)

    def nth(self, index):
        if 0 <= index < len(self._texts):
            return _Element(text=self._texts[index], visible=True)
        return _Element(visible=False)


class _ActionLocator:
    """A locator for an always-present, inert affordance (prompt field / Send)."""

    def __init__(self, on_action=None):
        self._on_action = on_action

    def count(self):
        return 1

    @property
    def first(self):
        return _Element(on_action=self._on_action, visible=True)

    @property
    def last(self):
        return _Element(on_action=self._on_action, visible=True)


class _FakeChatPage:
    """Models a chronological chat transcript for one provider's selectors.

    ``prior_turns`` are the assistant turns already on the page (the context on a
    resumed thread). Clicking Send appends ``answer`` as a NEW newest turn,
    exactly as a live provider renders its reply below the earlier ones.
    """

    def __init__(self, selectors, *, prior_turns, answer, reveal_after_probes=0):
        self._response_sel = set(selectors.response_container.as_list())
        self._final_sel = set(
            selectors.final_answer_container.as_list()
            if selectors.final_answer_container is not None
            else ()
        )
        self._prompt_sel = set(selectors.prompt_input.as_list())
        self._submit_sel = set(selectors.submit_button.as_list())
        self.assistant_texts = list(prior_turns)
        self._answer = answer
        self.submitted = False
        # >0 models a reply that renders only after the reader has polled a few
        # times (the live case: the new turn is NOT on the page the instant we
        # submit). 0 appends synchronously on submit.
        self._reveal_after_probes = reveal_after_probes
        self._pending = None
        self._probes = 0

    def _submit(self):
        if self.submitted:
            return
        self.submitted = True
        # A live provider appends its reply as the newest turn, below the prior
        # ones -- immediately, or (deferred) once the reader has polled a while.
        if self._reveal_after_probes <= 0:
            self.assistant_texts.append(self._answer)
        else:
            self._pending = self._answer

    def _maybe_reveal(self):
        if self._pending is None:
            return
        self._probes += 1
        if self._probes >= self._reveal_after_probes:
            self.assistant_texts.append(self._pending)
            self._pending = None

    def locator(self, selector):
        if selector in self._prompt_sel:
            return _ActionLocator()
        if selector in self._submit_sel:
            return _ActionLocator(on_action=self._submit)
        if selector in self._final_sel or selector in self._response_sel:
            self._maybe_reveal()
            return _ListLocator(self.assistant_texts)
        # streaming indicator / anything else: not present.
        return _ListLocator([])

    @property
    def url(self):
        return "https://provider.test/c/fake-thread"

    def is_closed(self):
        return False


def _run_one_turn(selectors, *, prior_turns, answer, reveal_after_probes=0):
    """Mirror the engine's submit -> stream -> read sequence on a fake page."""

    driver = PlaywrightPageDriver(response_settle_s=0.0)
    page = _FakeChatPage(
        selectors,
        prior_turns=prior_turns,
        answer=answer,
        reveal_after_probes=reveal_after_probes,
    )
    driver._page = page
    driver.submit_prompt(selectors, "what was the word?")
    streamed = "".join(driver.stream_response(selectors, timeout_s=5))
    final = driver.read_final_response(selectors, return_mode="final")
    return streamed, final


class TestResumedThreadReadsTheNewTurn:
    def test_reads_the_new_turn_not_the_prior_answer(self):
        """The pinned bug: turn 2 on a resumed thread must return turn 2's answer.

        With the old ``.first`` read this returns the prior turn's "OK".
        """
        streamed, final = _run_one_turn(
            CHATGPT_WEB, prior_turns=["OK"], answer="The word was BANANA."
        )

        assert final == "The word was BANANA."
        assert final != "OK"
        assert streamed == "The word was BANANA."

    def test_reads_the_newest_of_several_prior_turns(self):
        """Deeper history must not change which turn is read -- always the newest."""
        streamed, final = _run_one_turn(
            CHATGPT_WEB,
            prior_turns=["first answer", "second answer", "third answer"],
            answer="fourth answer",
        )

        assert final == "fourth answer"
        assert streamed == "fourth answer"

    def test_deepseek_final_answer_container_reads_newest_turn(self):
        """The reasoning-model final-answer path must also read the newest turn."""
        streamed, final = _run_one_turn(
            DEEPSEEK_WEB, prior_turns=["prior reply"], answer="42"
        )

        assert final == "42"

    def test_waits_for_the_reply_instead_of_returning_the_prior_turn(self):
        """The live case: the new turn is NOT on the page the instant we submit.

        The reader must POLL until this turn's node appears -- never latch the
        prior turn's already-rendered answer just because it is there first.
        """
        streamed, final = _run_one_turn(
            CHATGPT_WEB,
            prior_turns=["stale prior answer"],
            answer="fresh answer",
            reveal_after_probes=3,
        )

        assert final == "fresh answer"
        assert final != "stale prior answer"
        assert streamed == "fresh answer"


class TestSingleTurnUnchanged:
    def test_first_turn_reads_its_own_answer(self):
        """A fresh chat (no prior turns) is byte-identical: .last == .first."""
        streamed, final = _run_one_turn(
            CHATGPT_WEB, prior_turns=[], answer="pong"
        )

        assert final == "pong"
        assert streamed == "pong"
