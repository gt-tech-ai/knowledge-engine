"""Shared backend of the kb-registry tier: one configured KB for every key (the default topology)."""

from techai_webutils.clients.kb_registry.shared.registry import SharedKbRegistry

__all__ = ["SharedKbRegistry"]
