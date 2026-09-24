"""Tenant→KB registry builder — ``kb_registry_from_config`` (factory shape).

The tier-root factory: selects the registry backend by ``KbRegistryKind`` (``shared`` for the default
single-KB topology; ``output`` for per-org routing off the Terraform for_each map) and returns the
``TenantKbRegistry`` port. Unknown kinds — and a kind whose required config is absent (shared without
coordinates, output without a bindings map) — fail loudly with ``ValueError``, matching the
``foundation/logger`` ``NewFromConfig`` contract. A future
``db``/``grpc`` backend adds a ``KbRegistryKind`` here without changing the port.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry
from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry
from techai_webutils.clients.kb_registry.shared.registry import SharedKbRegistry

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.kb_registry import (
        KbBinding,
        KbCoordinates,
        TenantKbRegistry,
    )


class KbRegistryKind(StrEnum):
    """Which tenant→KB registry backend to build."""

    SHARED = "shared"
    """One configured KB for every workspace — the behavior-preserving default topology."""
    OUTPUT = "output"
    """Per-key ``KbBinding`` map loaded from the Terraform for_each output (per-org routing)."""
    ROSTER = "roster"
    """Per-org routing WITH a migration FSM: a listed ``migrating``/``provisioning`` org reads
    the shared KB (gapless), ``active`` reads its own, unmapped fails closed. Needs bindings + shared coords."""


@dataclass(frozen=True, slots=True)
class KbRegistryConfig:
    """Tenant→KB registry configuration (the impl is selected by ``kind``)."""

    kind: KbRegistryKind = KbRegistryKind.SHARED
    """Selects the backend."""
    shared_coordinates: KbCoordinates | None = None
    """The single KB the ``shared`` backend returns for every workspace (required for ``shared``)."""
    bindings: Mapping[str, KbBinding] = field(default_factory=dict)
    """The per-key binding map the ``output`` backend resolves over (required non-empty for ``output``)."""


def kb_registry_from_config(config: KbRegistryConfig) -> TenantKbRegistry:
    """Build the registry selected by ``config.kind``.

    ``shared`` requires ``shared_coordinates``; ``output`` requires a non-empty ``bindings`` map — each
    absence is a loud wiring bug (``ValueError``), never a silent fallback that would misroute tenants.
    Both backends satisfy the full ``TenantKbRegistry`` port, so a second ``KbRegistryKind`` returns the
    same type (parity with the Go ``NewFromConfig`` factories).
    """
    if config.kind is KbRegistryKind.SHARED:
        if config.shared_coordinates is None:
            msg = "shared kb registry requires shared_coordinates"
            raise ValueError(msg)
        return SharedKbRegistry(config.shared_coordinates)
    if config.kind is KbRegistryKind.OUTPUT:
        if not config.bindings:
            msg = "output kb registry requires a non-empty bindings map"
            raise ValueError(msg)
        return OutputBackedKbRegistry(config.bindings)
    if config.kind is KbRegistryKind.ROSTER:
        # Roster needs BOTH the per-org bindings (with status) AND the shared coords non-active orgs
        # fall back to — each absence is a loud wiring bug, never a silent misroute.
        if not config.bindings:
            msg = "roster kb registry requires a non-empty bindings map"
            raise ValueError(msg)
        if config.shared_coordinates is None:
            msg = "roster kb registry requires shared_coordinates (the migrating-org fallback KB)"
            raise ValueError(msg)
        return RosterKbRegistry(config.bindings, config.shared_coordinates)
    msg = f"unknown kb registry kind: {config.kind!r}"
    raise ValueError(msg)
