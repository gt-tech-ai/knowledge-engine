"""Proxy decorators for logging, tracing, retry, and GenAI span enrichment."""

from techai_webutils.clients.decorators.ai_enricher import GEN_AI_SYSTEM_BY_PROVIDER, AiSpanEnricher

__all__ = ["GEN_AI_SYSTEM_BY_PROVIDER", "AiSpanEnricher"]
