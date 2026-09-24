"""Tenant→KB registry port — resolves an isolation unit to its Bedrock KB coordinates.

The single seam both the ingestion and retrieval paths resolve through: the write path fans out one
lock-guarded KB-sync runner per ``list_active()`` binding, and the read path resolves a request's org to
the KB it must query. The interface is keyed by a routing key (``org_external_id`` for the per-org
topology) — so per-org / per-BU / per-tier grouping is a registry *population policy*, not an
interface change. Concrete backends live in ``techai_webutils.clients.kb_registry`` and are selected by
``kb_registry_from_config`` (``shared`` default; ``output`` per-key map from the Terraform for_each
output). Async so a future ``db``/``grpc`` backend needs no interface change,
matching the async-first ``RetrievalEngine`` / ``KnowledgeBaseIngestor`` ports.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol, runtime_checkable

# Binding lifecycle status values (a small FSM). ``active`` is the ONLY status the registry routes on
# — a read resolves and a write fans out solely for an active binding; ``provisioning``/``migrating``
# bindings are visible to operators but MUST fail closed on both paths (the tenant still reads the
# shared KB until cutover flips the binding to ``active``). Kept as strings (not an enum) to match the
# persisted-status vocabulary the Terraform output / identity table already carries.
ACTIVE_STATUS = "active"
"""The only status the registry routes on — reads resolve and writes fan out solely for active bindings."""
PROVISIONING_STATUS = "provisioning"
"""A binding still being provisioned; visible to operators but fails closed on both paths (reads/writes)."""
MIGRATING_STATUS = "migrating"
"""A binding mid-migration; fails closed on both paths until cutover flips it to ``active``."""


@dataclass(frozen=True, slots=True)
class KbCoordinates:
    """The Bedrock KB coordinates an isolation unit resolves to (the read-path result)."""

    knowledge_base_id: str
    """The Bedrock KB the unit's documents are indexed in + queried from."""
    data_source_id: str
    """The KB's S3 data source (the Bedrock serialization + lock key)."""
    s3_prefix: str = ""
    """The unit's single scanned object-key prefix (the data source's ONE ``inclusion_prefixes`` entry).

     the per-org layout is ``{org_external_id}/{workspace}/…``, so a per-org KB scopes to
    ``{org_external_id}/`` — a single prefix, the only per-org scoping Bedrock allows
    (``inclusionPrefixes`` is capped at exactly 1 item). The ``shared`` topology leaves this empty
    (retrieval routes by ``knowledge_base_id``; the migration never runs on shared).
    """


@dataclass(frozen=True, slots=True)
class KbBinding:
    """A registry binding: one isolation-unit key → its KB coordinates + lifecycle status.

    ``list_active()`` returns the ``active`` bindings the write path fans out over; ``tenant_key`` is
    the grouping key (org/BU/workspace) the binding was provisioned for.
    """

    tenant_key: str
    """The isolation-unit grouping key this binding was provisioned for (e.g. an org id)."""
    coordinates: KbCoordinates
    """The Bedrock KB this binding routes to."""
    status: str
    """One of the ACTIVE/PROVISIONING/MIGRATING status constants (only ``active`` routes)."""


@runtime_checkable
class TenantKbRegistry(Protocol):
    """Resolves an isolation unit (keyed by ``workspace_id``) to its Bedrock KB coordinates.

    The contract both the write fan-out and the per-request read path depend on. Backends are pure
    (in-memory ``shared``/``output`` today) or I/O-backed (a deferred ``db``/``grpc`` backend); the
    factory ``kb_registry_from_config`` selects one. Resolving an unknown or non-``active`` binding MUST
    fail closed (return ``None``) — never fall through to another tenant's KB (the security invariant).
    """

    async def resolve(self, key: str) -> KbCoordinates | None:
        """Return the ``active`` KB coordinates for a routing key, or ``None`` (fail closed) if unknown.

        The key is the isolation-unit routing key — ``org_external_id`` for the per-org ``output``
        topology; the ``shared`` backend ignores it and returns the one configured KB.
        """
        ...

    async def list_active(self) -> list[KbBinding]:
        """Return every ``active`` binding this registry owns (the write-path fan-out set)."""
        ...
