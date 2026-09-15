"""Event publisher interface for domain events.

Mirrors Go's ``interfaces.EventPublisher``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.events.events import DomainEvent


class EventPublisher(ABC):
    """Publishes domain events, typically via a transactional outbox."""

    @abstractmethod
    async def publish(self, event: DomainEvent) -> None:
        """Publish a single domain event."""
        ...

    @abstractmethod
    async def publish_batch(self, events: list[DomainEvent]) -> None:
        """Publish multiple domain events."""
        ...
