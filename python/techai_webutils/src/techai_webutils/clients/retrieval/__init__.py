"""Retrieval-engine clients (Go Layer-2 adapters behind ``core.interfaces.retrieval.RetrievalEngine``).

The raw engines — Bedrock Knowledge Base (stage/prod) + a deterministic stub (dev) — the generic
clearance-filtering ``FilteringRetrievalEngine`` decorator, and the env-aware
``new_retrieval_engine_from_config`` factory. Apps select an engine via config and depend only on the
interface.
"""
