"""Inbound routing decisions made before the conversation starts (docs/API.md, Inbound routing).

* :func:`gate_for` — whether the call must be refused up front: ``quota_exceeded``
  (``Bootstrap.entitlements.canStart`` is false) or ``after_hours`` (route ``after_hours``
  without a profile). The caller then only hears :attr:`CallGate.message`.
* :class:`MenuFlow` — the IVR menu (route ``menu``): speak ``menuPrompt``, wait for a key
  (SIP DTMF, or a spoken digit / option label as a fallback), repeat up to ``menuRepeat``
  times and return the chosen :class:`~callgo_agent.schemas.MenuOption` (``None`` on
  timeout: the caller continues with the default profile).
* :class:`KeyQueue` — the injectable key source; :func:`attach_dtmf` feeds it from
  ``rtc.Room`` ``sip_dtmf_received`` and :func:`attach_spoken_keys` from final transcripts.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import re
from collections.abc import Awaitable, Callable, Sequence
from dataclasses import dataclass
from typing import Any, Literal, Protocol

from .schemas import Bootstrap, CallDirection, EndReason, MenuOption, ResolvedRoute

log = logging.getLogger("callgo.routing")

QUOTA_MESSAGE = "Уучлаарай, одоогоор үйлчилгээ авах боломжгүй байна."
AFTER_HOURS_MESSAGE = (
    "Уучлаарай, одоо ажлын цаг бус байна. Ажлын цагаар дахин холбогдоно уу. Баярлалаа."
)
MENU_INVALID_MESSAGE = "Уучлаарай, буруу товчлуур байна."
MENU_TIMEOUT_MIN_SEC = 1.0
VALID_KEYS = frozenset("0123456789*#")
# Spoken answers are only trusted when short: "хоёр", "хоёрыг дарна", "борлуулалт".
SPOKEN_MAX_WORDS = 5

_DIGIT_WORDS: dict[str, str] = {}
for _key, _forms in (
    ("0", "тэг тэгийг"),
    ("1", "нэг нэгийг нэгдүгээр нэгд нэгэн"),
    ("2", "хоёр хоёрыг хоёрдугаар хоёрт"),
    ("3", "гурав гурвыг гуравдугаар гуравт гурван"),
    ("4", "дөрөв дөрвийг дөрөвдүгээр дөрөвт дөрвөн"),
    ("5", "тав тавыг тавдугаар тавд таван"),
    ("6", "зургаа зургааг зургаадугаар зургаад зургаан"),
    ("7", "долоо долоог долоодугаар долоод долоон"),
    ("8", "найм наймыг наймдугаар наймд найман"),
    ("9", "ес есийг есдүгээр есд есөн"),
    ("*", "од одыг"),
    ("#", "тор торыг"),
):
    for _form in _forms.split():
        _DIGIT_WORDS[_form] = _key
_WORD_RE = re.compile(r"[\w*#]+")


# ---- gates ----------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class CallGate:
    """The call is refused with ``message`` and ends with ``end_reason``."""

    end_reason: EndReason
    message: str


def gate_for(bootstrap: Bootstrap) -> CallGate | None:
    """``quota_exceeded`` / ``after_hours`` refusal for this call, or ``None`` to continue."""
    if not bootstrap.entitlements.can_start:
        return CallGate("quota_exceeded", QUOTA_MESSAGE)
    route = inbound_route(bootstrap)
    if route is not None and route.mode == "after_hours" and route.agent_profile_id is None:
        return CallGate("after_hours", route.message.strip() or AFTER_HOURS_MESSAGE)
    return None


def inbound_route(bootstrap: Bootstrap) -> ResolvedRoute | None:
    """``bootstrap.route`` for inbound calls (outbound calls are never routed)."""
    if bootstrap.call.direction != CallDirection.INBOUND:
        return None
    return bootstrap.route


def menu_route(bootstrap: Bootstrap) -> ResolvedRoute | None:
    """The route when this inbound call must go through the IVR menu first."""
    route = inbound_route(bootstrap)
    if route is None or route.mode != "menu" or not route.menu:
        return None
    return route


# ---- keys -----------------------------------------------------------------------------


def normalize_key(value: str) -> str:
    """``"1"``, ``" 1 "`` -> ``"1"``; anything that is not a single menu key -> ``""``."""
    key = value.strip()
    return key if key in VALID_KEYS else ""


def spoken_key(text: str, options: Sequence[MenuOption] = ()) -> str:
    """Menu key from a short spoken answer: a digit (``"2"``, ``"хоёр"``, ``"хоёрыг"``)
    or an option label (``"Борлуулалт"``). ``""`` when nothing matches."""
    words = _WORD_RE.findall(text.casefold())
    if not words or len(words) > SPOKEN_MAX_WORDS:
        return ""
    for word in words:
        if len(word) == 1 and word in VALID_KEYS:
            return word
        if word in _DIGIT_WORDS:
            return _DIGIT_WORDS[word]
    folded = " ".join(words)
    for option in options:
        label = " ".join(_WORD_RE.findall(option.label.casefold()))
        if len(label) >= 3 and label in folded:
            return option.key
    return ""


KeySource = Literal["dtmf", "speech"]


class KeyQueue:
    """Keys pressed or spoken by the caller, consumed by :class:`MenuFlow`."""

    def __init__(self) -> None:
        self._queue: asyncio.Queue[tuple[str, KeySource]] = asyncio.Queue()

    def push(self, key: str, source: KeySource = "dtmf") -> bool:
        k = normalize_key(key)
        if not k:
            return False
        self._queue.put_nowait((k, source))
        return True

    def clear(self) -> None:
        while not self._queue.empty():
            self._queue.get_nowait()

    async def get(self) -> tuple[str, KeySource]:
        return await self._queue.get()


class _Emitter(Protocol):
    def on(self, event: str, callback: Callable[..., Any]) -> Any: ...

    def off(self, event: str, callback: Callable[..., Any]) -> Any: ...


def attach_dtmf(room: _Emitter, keys: KeyQueue, identity: str = "") -> Callable[[], None]:
    """Feed ``rtc.Room`` ``sip_dtmf_received`` (``rtc.SipDTMF``) digits into ``keys``.

    With ``identity`` only digits from that participant (the SIP caller) count. Returns a
    function that detaches the listener.
    """

    def on_dtmf(ev: Any) -> None:
        participant = getattr(ev, "participant", None)
        if identity and participant is not None and participant.identity != identity:
            return
        digit = str(getattr(ev, "digit", "") or "")
        if keys.push(digit, "dtmf"):
            log.info("DTMF %r received", digit)

    room.on("sip_dtmf_received", on_dtmf)

    def detach() -> None:
        with contextlib.suppress(Exception):
            room.off("sip_dtmf_received", on_dtmf)

    return detach


def attach_spoken_keys(
    session: _Emitter, keys: KeyQueue, options: Sequence[MenuOption]
) -> Callable[[], None]:
    """Feed final ``user_input_transcribed`` transcripts that name a key into ``keys``."""

    def on_transcript(ev: Any) -> None:
        if not getattr(ev, "is_final", False):
            return
        key = spoken_key(str(getattr(ev, "transcript", "") or ""), options)
        if key and keys.push(key, "speech"):
            log.info("spoken menu key %r", key)

    session.on("user_input_transcribed", on_transcript)

    def detach() -> None:
        with contextlib.suppress(Exception):
            session.off("user_input_transcribed", on_transcript)

    return detach


# ---- menu -----------------------------------------------------------------------------

PromptPlayer = Callable[[str], Awaitable[None]]
"""Speak a text and return once it has played out; cancelling it must stop the speech."""


@dataclass(slots=True)
class MenuResult:
    option: MenuOption | None
    reason: Literal["selected", "timeout"]
    key: str = ""
    source: KeySource | Literal[""] = ""
    attempts: int = 0


class MenuFlow:
    """Speak the menu prompt and wait for a valid key (see module docs).

    Every prompt counts as one attempt (``1 + menuRepeat`` in total); a key pressed while
    the prompt plays interrupts it. An unknown key says :data:`MENU_INVALID_MESSAGE` and
    repeats the prompt. The answer window of ``menuTimeoutSec`` starts after the prompt.
    """

    def __init__(
        self,
        route: ResolvedRoute,
        play: PromptPlayer,
        keys: KeyQueue,
        *,
        invalid_message: str = MENU_INVALID_MESSAGE,
    ) -> None:
        self.route = route
        self._play = play
        self._keys = keys
        self._invalid_message = invalid_message
        self._options = {o.key.strip(): o for o in route.menu if o.key.strip()}

    @property
    def attempts(self) -> int:
        return 1 + max(0, self.route.menu_repeat)

    @property
    def timeout(self) -> float:
        return max(MENU_TIMEOUT_MIN_SEC, float(self.route.menu_timeout_sec))

    def prompt_text(self) -> str:
        """``menuPrompt``, or one generated from the option labels when it is empty."""
        if self.route.menu_prompt.strip():
            return self.route.menu_prompt.strip()
        parts = [f"{o.label or 'Сонголт'} бол {o.key}" for o in self._options.values()]
        return "Сонголтоо хийнэ үү. " + ", ".join(parts) + " дугаарыг дарна уу."

    async def run(self) -> MenuResult:
        prompt = self.prompt_text()
        for attempt in range(1, self.attempts + 1):
            got = await self._ask(prompt)
            if got is None:
                log.info("menu attempt %d/%d: no key", attempt, self.attempts)
                continue
            key, source = got
            option = self._options.get(key)
            if option is not None:
                log.info("menu option %r selected by %s", key, source)
                return MenuResult(option, "selected", key, source, attempt)
            log.info("menu attempt %d/%d: unknown key %r", attempt, self.attempts, key)
            if attempt < self.attempts and self._invalid_message:
                await self._say_quietly(self._invalid_message)
        return MenuResult(None, "timeout", attempts=self.attempts)

    async def _say_quietly(self, text: str) -> None:
        try:
            await self._play(text)
        except asyncio.CancelledError:
            raise
        except Exception as exc:  # noqa: BLE001 - the menu keeps going without audio
            log.warning("menu speech failed: %s", exc)

    async def _ask(self, prompt: str) -> tuple[str, KeySource] | None:
        """Play the prompt and wait for one key; ``None`` after the answer window."""
        self._keys.clear()
        play = asyncio.ensure_future(self._say_quietly(prompt))
        get = asyncio.ensure_future(self._keys.get())
        try:
            done, _ = await asyncio.wait({play, get}, return_when=asyncio.FIRST_COMPLETED)
            if get not in done:
                done, _ = await asyncio.wait({get}, timeout=self.timeout)
            if get in done:
                return get.result()
            return None
        finally:
            for task in (play, get):
                if not task.done():
                    task.cancel()
            await asyncio.gather(play, get, return_exceptions=True)


class SessionPromptPlayer:
    """:data:`PromptPlayer` backed by ``AgentSession.say`` (interrupted on cancellation)."""

    def __init__(self, session: Any, *, playout_timeout: float = 60.0) -> None:
        self._session = session
        self._timeout = playout_timeout

    async def __call__(self, text: str) -> None:
        handle = self._session.say(text, allow_interruptions=True)
        try:
            await asyncio.wait_for(handle.wait_for_playout(), timeout=self._timeout)
        except asyncio.CancelledError:
            with contextlib.suppress(Exception):
                handle.interrupt(force=True)
            raise
