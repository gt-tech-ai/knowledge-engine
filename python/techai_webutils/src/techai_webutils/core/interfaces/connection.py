"""WebSocket connection manager interface.

Mirrors Go's ``interfaces.ConnectionManager`` and ``ClientConnection``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass


@dataclass
class ClientConnection:
    """An active WebSocket connection."""

    id: str
    """Unique identifier for this connection, used to address it in ``send``/``unregister``."""
    user_id: str
    """The authenticated user who owns this connection (target of ``send_to_user``)."""
    org_id: str
    """The tenant/organization the connection belongs to."""
    workspace_id: str
    """The workspace this connection is subscribed to (the ``broadcast`` fan-out key)."""


class ConnectionManager(ABC):
    """Handles WebSocket connection lifecycle and message routing."""

    @abstractmethod
    async def register(self, conn: ClientConnection) -> None:
        """Add a new client connection."""
        ...

    @abstractmethod
    async def unregister(self, connection_id: str) -> None:
        """Remove a client connection."""
        ...

    @abstractmethod
    async def send(self, connection_id: str, msg: bytes) -> None:
        """Send a message to a specific connection."""
        ...

    @abstractmethod
    async def broadcast(self, workspace_id: str, msg: bytes) -> None:
        """Send a message to all connections in a workspace."""
        ...

    @abstractmethod
    async def send_to_user(self, user_id: str, msg: bytes) -> None:
        """Send a message to all connections of a specific user."""
        ...

    @abstractmethod
    async def active_connections(self, workspace_id: str) -> int:
        """Return the count of active connections for a workspace."""
        ...
