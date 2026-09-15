"""``SharedKbRegistry`` — the default topology: every workspace resolves to one configured KB.

Selected by ``KbRegistryKind.SHARED`` (the default) when per-org KBs are not provisioned: ``resolve``
returns the single configured ``KbCoordinates`` for any workspace, and ``list_active`` yields exactly
one ``active`` binding, so the ingestion worker keeps its single global KB-sync loop. This preserves
today's single-KB behavior until per-org provisioning + migration land (design §6). Mirrors
``NoopIngestionJobStateStore`` (a pure, I/O-free backend of a config-selected tier).
"""

from __future__ import annotations

from techai_webutils.core.interfaces.kb_registry import (
    ACTIVE_STATUS,
    KbBinding,
    KbCoordinates,
)


class SharedKbRegistry:
    """Resolves every routing key to one configured KB (the behavior-preserving default topology)."""

    def __init__(self, coordinates: KbCoordinates, *, tenant_key: str = "shared") -> None:
        """Wire the one shared KB + the single ``active`` binding ``list_active`` advertises."""
        self._coordinates = coordinates
        self._binding = KbBinding(tenant_key=tenant_key, coordinates=coordinates, status=ACTIVE_STATUS)

    async def resolve(self, key: str) -> KbCoordinates | None:  # noqa: ARG002 - shared topology returns the one KB for every routing key
        """Return the single configured KB coordinates for any routing key (shared topology)."""
        return self._coordinates

    async def list_active(self) -> list[KbBinding]:
        """Return the single ``active`` binding — one global KB-sync runner on the write path."""
        return [self._binding]
