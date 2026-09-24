"""Tenant→KB registry client — config-selected backends behind ``TenantKbRegistry``.

Resolves an isolation unit (keyed by ``workspace_id``) to its Bedrock KB coordinates so ingestion fans
out per KB and retrieval routes per request. Pure in-memory backends ship — ``shared`` (one KB for
every key, the behavior-preserving default), ``output`` (a per-key ``KbBinding`` map loaded from
the Terraform for_each output at deploy), and ``roster`` (``output`` plus a per-binding status) — selected by
``kb_registry_from_config``. Mirrors the client tier-root factory shape.
"""

from techai_webutils.clients.kb_registry.builder import (
    KbRegistryConfig,
    KbRegistryKind,
    kb_registry_from_config,
)
from techai_webutils.clients.kb_registry.output import OutputBackedKbRegistry
from techai_webutils.clients.kb_registry.shared import SharedKbRegistry
from techai_webutils.core.interfaces.kb_registry import (
    ACTIVE_STATUS,
    MIGRATING_STATUS,
    PROVISIONING_STATUS,
    KbBinding,
    KbCoordinates,
    TenantKbRegistry,
)

__all__ = [
    "ACTIVE_STATUS",
    "MIGRATING_STATUS",
    "PROVISIONING_STATUS",
    "KbBinding",
    "KbCoordinates",
    "KbRegistryConfig",
    "KbRegistryKind",
    "OutputBackedKbRegistry",
    "SharedKbRegistry",
    "TenantKbRegistry",
    "kb_registry_from_config",
]
