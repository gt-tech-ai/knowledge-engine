"""``OutputBackedKbRegistry`` — a per-key ``KbBinding`` map loaded from the Terraform for_each output.

Selected by ``KbRegistryKind.OUTPUT`` for per-org routing: the module's ``map(org → coords)`` output is
serialized into config at deploy (bindings change only on ``terraform apply``, design §4), and this
backend does a pure in-memory lookup over it — no runtime I/O. The map is keyed by ``org_external_id``
(the Auth0 org id — the Terraform ``per_org_kbs`` key), the same string the retrieval read path resolves
by (the Go API puts it on ``QueryStreamRequest.org_id``, 32.9), so no workspace→org expansion is needed.
Resolving an unknown key or a non-``active`` binding fails **closed** (returns ``None``), never falling
through to another tenant's KB (the security invariant, design §3).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.kb_registry import ACTIVE_STATUS

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.kb_registry import KbBinding, KbCoordinates


class OutputBackedKbRegistry:
    """Resolves a routing key (``org_external_id``) to its KB via a per-key ``KbBinding`` map (no I/O)."""

    def __init__(self, bindings: Mapping[str, KbBinding]) -> None:
        """Wire the org-keyed binding map (a defensive copy — the registry never mutates it)."""
        self._bindings: dict[str, KbBinding] = dict(bindings)

    async def resolve(self, key: str) -> KbCoordinates | None:
        """Return the routing key's KB coordinates, or ``None`` (fail closed) if unknown or not active."""
        binding = self._bindings.get(key)
        if binding is None or binding.status != ACTIVE_STATUS:
            return None
        return binding.coordinates

    async def list_active(self) -> list[KbBinding]:
        """Return every ``active`` binding — the KBs the write path fans a sync runner out over."""
        return [binding for binding in self._bindings.values() if binding.status == ACTIVE_STATUS]
