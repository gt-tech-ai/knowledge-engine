"""Shared fixtures for the flat unit suite.

Merged home for the per-layer unit fixtures that previously lived in the
now-collapsed ``tests/<layer>/conftest.py`` files:

- ``clean_prefixed_env_vars`` / ``mock_redis`` — from the former ``foundation/conftest.py``.
- ``s3_config`` / ``sqs_config`` / ``mock_service`` — from the former ``clients/conftest.py``.

The five fixture names are disjoint, so the merge introduces no collisions. The
integration suite keeps its own ``sqs_config`` / ``s3_config`` (real ElasticMQ/MinIO)
in ``tests/integration/conftest.py``, resolved independently for that directory.
"""

from __future__ import annotations

import os
from unittest.mock import AsyncMock, MagicMock

from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.storage.config import S3Config
import pytest


@pytest.fixture(autouse=True)
def clean_prefixed_env_vars(monkeypatch: pytest.MonkeyPatch) -> None:
    """Clean all MYAPP_* environment variables (the tests' config prefix) around each test.

    This fixture runs automatically for all tests in this directory.
    It ensures test isolation by removing any MYAPP_* env vars that
    might have been set by previous tests or the test environment.
    """
    # Clean before test
    for key in list(os.environ.keys()):
        if key.startswith("MYAPP_"):
            monkeypatch.delenv(key, raising=False)

    # Reset config sentinel for clean state
    try:
        from techai_webutils.foundation.config.bridge import reset_config

        reset_config()
    except ImportError:
        pass

    yield

    # Clean after test
    for key in list(os.environ.keys()):
        if key.startswith("MYAPP_"):
            monkeypatch.delenv(key, raising=False)

    # Reset config sentinel again
    try:
        from techai_webutils.foundation.config.bridge import reset_config

        reset_config()
    except ImportError:
        pass


@pytest.fixture
def mock_redis() -> AsyncMock:
    """Provide a mock async Redis client for cache tests."""
    return AsyncMock()


@pytest.fixture
def s3_config() -> S3Config:
    return S3Config(
        endpoint="http://localhost:9000",
        bucket="test-bucket",
        region="us-east-1",
        access_key="minioadmin",
        secret_key="minioadmin",
    )


@pytest.fixture
def sqs_config() -> SQSConfig:
    return SQSConfig(
        endpoint="http://localhost:9324",
        region="us-east-1",
        queue_url="http://localhost:9324/queue/test",
        access_key="local",
        secret_key="local",
    )


@pytest.fixture
def mock_service() -> MagicMock:
    """A MagicMock service reproducing the canned returns the proxies forward.

    ``get_item(id)`` -> ``{"id": id, "name": "test"}``; ``create_item(name)`` ->
    ``{"id": "new", "name": name}``; ``failing_method()`` raises ``RuntimeError``, so the
    proxy tests can assert transparent forwarding, chained composition, and error propagation.
    """
    svc = MagicMock()
    svc.get_item.side_effect = lambda item_id: {"id": item_id, "name": "test"}
    svc.create_item.side_effect = lambda name: {"id": "new", "name": name}
    svc.failing_method.side_effect = RuntimeError("intentional failure")
    return svc
