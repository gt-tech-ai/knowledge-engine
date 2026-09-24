"""Single-writer lock client package (client-package shape).

``InMemoryLock`` (single-process; dev / replica=1) and ``PostgresAdvisoryLock`` (cross-pod;
stage/prod) are interchangeable ``DistributedLock`` backends selected by ``new_lock_from_config``.
``SingleWriterRunner`` runs an async op under whichever backend the config picks and skips
(returns ``None``) when another writer holds the lock. Postgres is the low-cardinality / long-hold
sibling of a Redis lock backend — same abstraction, backend chosen by profile.

Cross-cutting concerns (logging, tracing, retry, circuit breaking) are layered onto a backend's
``acquire``/``release`` by the generic ``clients/decorators`` proxies at the composition root, not
inside the backends themselves.
"""

from techai_webutils.clients.lock.builder import LockConfig, LockKind, new_lock_from_config
from techai_webutils.clients.lock.memory import InMemoryLock
from techai_webutils.clients.lock.runner import SingleWriterRunner

__all__ = ["InMemoryLock", "LockConfig", "LockKind", "SingleWriterRunner", "new_lock_from_config"]
