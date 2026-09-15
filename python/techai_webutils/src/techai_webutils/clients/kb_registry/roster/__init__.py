"""Roster-backed tenant→KB registry (per-org routing with a gapless migration FSM)."""

from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry

__all__ = ["RosterKbRegistry"]
