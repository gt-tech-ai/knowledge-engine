"""Retrieval-engine clients (adapters behind ``core.interfaces.retrieval.RetrievalEngine``).

The raw engines — Bedrock Knowledge Base, a local vector store and a stub — the policy-based
``FilteringRetrievalEngine`` decorator with its built-in ``PassagePolicy`` rules, and the
config-selected ``new_retrieval_engine_from_config`` factory. Apps select an engine via config and
depend only on the interface. The Bedrock seam types (``FilterBuilder``, ``DocumentIdResolver``,
``metadata_document_id``) are exported by ``clients.retrieval.bedrock``, which loads the AWS SDK.
"""

from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
    MinScore,
    OrdinalCeiling,
    PassagePolicy,
)
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine

__all__ = [
    "FilteringRetrievalEngine",
    "MetadataEquals",
    "MinScore",
    "OrdinalCeiling",
    "PassagePolicy",
    "RetrievalConfig",
    "RetrievalKind",
    "StubRetrievalEngine",
    "new_retrieval_engine_from_config",
]
