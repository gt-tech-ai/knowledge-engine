"""Retrieval-engine clients (adapters behind ``core.interfaces.retrieval.RetrievalEngine``).

The raw engines — Bedrock Knowledge Base, a local vector store and a stub — the policy-based
``FilteringRetrievalEngine`` decorator, and the config-selected ``new_retrieval_engine_from_config``
factory. Apps select an engine via config and depend only on the interface.
"""
