"""``RosterKbRegistry`` — per-org routing with a gapless, reversible migration roster.

Selected by ``KbRegistryKind.ROSTER`` for the per-org topology WITH an in-flight migration. It extends
the ``output`` backend's org-keyed map with a per-binding status FSM so a tenant migrates
without a read gap and cutover is per-tenant + reversible — all config/output-backed (bindings change
only on ``terraform apply`` / a redeploy), with **no** runtime-mutable ``db`` backend:

- ``active`` → the org's OWN KB (reads + the write fan-out route to it).
- ``migrating`` / ``provisioning`` → the SHARED KB coordinates: the org still reads the shared KB it is
  authoritative for while its per-org KB is being back-filled (gapless), and its live uploads keep
  indexing into the shared KB (the shared binding stays in ``list_active`` while any org is non-active).
- **unmapped** → ``None`` (**fail closed** — unchanged from ``output``; the security invariant protects
  an UNKNOWN org from ever reading another tenant's KB — this is NOT loosened, because a migrating org
  is EXPLICITLY listed and reads the shared KB it already owns, not another tenant's).

Per-org cutover = flip a binding's status ``provisioning → migrating → active`` (redeploy the map);
rollback = flip it back. The read path (``query_workflow``) is unchanged — it passes the resolved
``knowledge_base_id`` to the engine, and the ``workspace_id`` metadata filter stays as defense-in-depth.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.kb_registry import ACTIVE_STATUS, KbBinding

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.kb_registry import KbCoordinates

# The synthetic tenant key of the shared binding ``list_active`` advertises while any org is migrating,
# so the shared KB-sync runner keeps indexing migrating orgs' live uploads (matches ``SharedKbRegistry``).
_SHARED_TENANT_KEY = "shared"
"""Synthetic tenant key the shared binding advertises while any org is migrating."""

# Non-active statuses that route READS to the shared KB (gapless) rather than failing closed — an org in
# these states is authoritative on the shared KB until its per-org KB is back-filled + cut over.
_SHARED_FALLBACK_STATUSES = frozenset({"provisioning", "migrating"})
"""Org statuses that route reads to the shared KB (gapless) instead of failing closed."""


class RosterKbRegistry:
    """Resolves an org to its KB via a status roster: active→own, migrating/provisioning→shared, else None."""

    def __init__(self, bindings: Mapping[str, KbBinding], shared_coordinates: KbCoordinates) -> None:
        """Wire the org-keyed binding map (a defensive copy) + the shared KB the non-active orgs fall to."""
        self._bindings: dict[str, KbBinding] = dict(bindings)
        self._shared = shared_coordinates

    async def resolve(self, key: str) -> KbCoordinates | None:
        """Return the org's routed KB: own when ``active``, the shared KB when migrating/provisioning.

        An unknown org (not in the roster) fails **closed** (``None``) — never another tenant's KB. A
        listed org that is mid-migration reads the SHARED KB it is still authoritative for (gapless),
        which is not a fail-closed violation (it is the org's own current data, explicitly configured).
        """
        binding = self._bindings.get(key)
        if binding is None:
            return None
        if binding.status == ACTIVE_STATUS:
            return binding.coordinates
        if binding.status in _SHARED_FALLBACK_STATUSES:
            return self._shared
        return None

    async def list_active(self) -> list[KbBinding]:
        """Return the write-path fan-out set (active own-KB bindings + shared while any org migrates).

        Every ``active`` org's own-KB binding, PLUS the shared binding whenever any listed org is still
        non-active (so a migrating org's live uploads keep indexing into the shared KB it reads). Once
        every org is ``active`` the shared binding drops out.
        """
        active = [b for b in self._bindings.values() if b.status == ACTIVE_STATUS]
        if any(b.status != ACTIVE_STATUS for b in self._bindings.values()):
            active.append(KbBinding(_SHARED_TENANT_KEY, self._shared, ACTIVE_STATUS))
        return active
