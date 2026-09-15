"""Noop email backend — logs the send without dispatching (the default / dev-stub path)."""

from __future__ import annotations

from techai_webutils.foundation.logger import get_logger

logger = get_logger(__name__)


class NoopEmailSender:
    """An EmailSender that logs instead of sending (implements the EmailSender port)."""

    async def send(self, *, to: str, subject: str, html_body: str) -> None:  # noqa: ARG002
        """Log the suppressed send (the html body is intentionally not logged)."""
        logger.info("email suppressed (noop backend)", to=to, subject=subject)
