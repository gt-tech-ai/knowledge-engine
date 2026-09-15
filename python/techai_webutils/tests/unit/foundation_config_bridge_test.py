"""Tests for YAML-to-env bridge (initialize_config and apply_yaml_defaults).

Why these tests are important:
  - The bridge is the interface between YAML config files and Pydantic Settings
  - It must handle missing config directories gracefully (Docker/K8s deployments)
  - Environment variables must take precedence over YAML values (12-factor)
  - Idempotency is critical for test isolation and multi-call scenarios
"""

import os
from pathlib import Path
from tempfile import TemporaryDirectory

import pytest


def test_initialize_config_sets_env_vars_from_yaml(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that initialize_config() loads YAML and exports SEARCH_* env vars.

    **Why this test is important:**
      - This is the primary job of the bridge: it is the only path by which
        YAML config reaches Pydantic Settings, which read solely from env vars
      - If the dotted-path-to-env-var mapping broke, services would silently
        fall back to defaults instead of honoring base.yaml

    **What it tests:**
      - YAML keys are flattened to SEARCH_* env vars (every scalar leaf)
      - Dotted paths like database.host become SEARCH_DATABASE_HOST, with both
        host and port from the database and redis sections exported
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange: Create temp config directory with YAML
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: test-db-host
  port: 5555
redis:
  host: test-redis-host
  port: 6380
""")

        # Clear any existing env vars
        for key in list(os.environ.keys()):
            if key.startswith("SEARCH_"):
                monkeypatch.delenv(key, raising=False)

        # Act
        initialize_config(tmpdir)

        # Assert
        assert os.environ.get("SEARCH_DATABASE_HOST") == "test-db-host"
        assert os.environ.get("SEARCH_DATABASE_PORT") == "5555"
        assert os.environ.get("SEARCH_REDIS_HOST") == "test-redis-host"
        assert os.environ.get("SEARCH_REDIS_PORT") == "6380"

        # Cleanup
        reset_config()


def test_initialize_config_merges_environment_overlay(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that initialize_config() merges the {env}.yaml overlay and flattens every key.

    **Why this test is important:**
      - In-cluster the services mount base.yaml + {env}.yaml and rely on the overlay to
        flip stub/localhost defaults to the real backend (e.g. ingestion.kb.kind=bedrock,
        storage.s3.endpoint=""). If the overlay is not merged, or a nested key the old
        hand-maintained map omitted (e.g. retrieval.llm.*) is dropped, the service silently
        runs against stub/localhost — the exact staging outage this fix addresses.

    **What it tests:**
      - With SEARCH_ENV set, base.yaml + {env}.yaml deep-merge (overlay wins on conflict).
      - A base-only key survives; a deeply nested key never in the old map is exported.
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    with TemporaryDirectory() as tmpdir:
        (Path(tmpdir) / "base.yaml").write_text("""
retrieval:
  llm:
    kind: stub
    model: base-model
  kb:
    kind: stub
""")
        (Path(tmpdir) / "staging.yaml").write_text("""
retrieval:
  llm:
    kind: bedrock
  kb:
    kind: bedrock
""")
        for key in [k for k in os.environ if k.startswith("SEARCH_")]:
            monkeypatch.delenv(key, raising=False)
        monkeypatch.setenv("SEARCH_ENV", "staging")

        initialize_config(tmpdir)

        # Overlay wins on conflict...
        assert os.environ.get("SEARCH_RETRIEVAL_LLM_KIND") == "bedrock"
        assert os.environ.get("SEARCH_RETRIEVAL_KB_KIND") == "bedrock"
        # ...base-only key survives the merge; nested key (never in the old map) is exported.
        assert os.environ.get("SEARCH_RETRIEVAL_LLM_MODEL") == "base-model"

        reset_config()


def test_initialize_config_env_var_precedence(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that an env var set before initialize_config() is not overwritten by YAML.

    **Why this test is important:**
      - 12-factor precedence (env over file) is the contract operators rely on
        to override any YAML default in staging/prod without editing files
      - If YAML clobbered a pre-set env var, a deployed override would be
        silently ignored — a dangerous, hard-to-diagnose misconfiguration

    **What it tests:**
      - A pre-set SEARCH_DATABASE_HOST keeps its value after initialize_config
      - Keys absent from the environment (database.port) are still populated
        from YAML
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: yaml-host
  port: 5432
""")

        # Set env var BEFORE initialize_config
        monkeypatch.setenv("SEARCH_DATABASE_HOST", "override-host")

        # Act
        initialize_config(tmpdir)

        # Assert: Pre-set env var is NOT overwritten
        assert os.environ.get("SEARCH_DATABASE_HOST") == "override-host"
        # Port from YAML is still set
        assert os.environ.get("SEARCH_DATABASE_PORT") == "5432"

        # Cleanup
        reset_config()


def test_initialize_config_missing_directory_graceful_fallback(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    """Test that initialize_config() degrades to a warning when the config dir is missing.

    **Why this test is important:**
      - Docker/K8s deployments often mount no config files and configure purely
        via env vars; a hard failure here would prevent services from starting
        in their primary production deployment mode
      - The warning (rather than a swallowed silence) keeps a genuine
        misconfiguration visible in logs

    **What it tests:**
      - initialize_config does not raise for a nonexistent directory
      - A "config directory not found" warning is emitted
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange: Use a directory that doesn't exist
    nonexistent_dir = "/nonexistent/config/path"

    # Act: Should not raise
    initialize_config(nonexistent_dir)

    # Assert: Warning logged, no exception
    assert "Config directory not found" in caplog.text or "does not exist" in caplog.text

    # Cleanup
    reset_config()


def test_initialize_config_idempotency(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that a second initialize_config() call is a no-op via the sentinel flag.

    **Why this test is important:**
      - Multiple modules and test fixtures may defensively call initialize_config;
        without idempotency, a later call could re-apply YAML and stomp values
        that were intentionally changed after the first initialization
      - Confirms the sentinel actually short-circuits re-execution

    **What it tests:**
      - After the first call set the value, a value modified between calls
        survives the second call (the second call does not re-apply YAML)
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: first-host
""")

        # Act: Call twice
        initialize_config(tmpdir)
        first_value = os.environ.get("SEARCH_DATABASE_HOST")

        # Modify env var between calls
        monkeypatch.setenv("SEARCH_DATABASE_HOST", "modified")

        initialize_config(tmpdir)  # Second call
        second_value = os.environ.get("SEARCH_DATABASE_HOST")

        # Assert: Second call is a no-op, modified value preserved
        assert first_value == "first-host"
        assert second_value == "modified"  # Not overwritten

        # Cleanup
        reset_config()


def test_reset_config_clears_sentinel(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that reset_config() clears the sentinel so initialize_config() runs again.

    **Why this test is important:**
      - The idempotency sentinel is process-global; without a reset hook, the
        first test to initialize would poison every later test's config state,
        making the suite order-dependent and unreliable
      - Verifies the escape hatch that re-enables re-initialization actually
        works (and re-reads the YAML afresh)

    **What it tests:**
      - After reset_config, a second initialize_config picks up newly-written
        YAML (second-run value), proving re-initialization occurred
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: first-run
""")

        # Act
        initialize_config(tmpdir)
        first_value = os.environ.get("SEARCH_DATABASE_HOST")

        # Clear sentinel
        reset_config()

        # Modify YAML
        config_file.write_text("""
database:
  host: second-run
""")

        # Clear env var to see if it gets set again
        monkeypatch.delenv("SEARCH_DATABASE_HOST", raising=False)

        # Re-initialize
        initialize_config(tmpdir)
        second_value = os.environ.get("SEARCH_DATABASE_HOST")

        # Assert: Second initialization worked
        assert first_value == "first-run"
        assert second_value == "second-run"

        # Cleanup
        reset_config()


def test_apply_yaml_defaults_stringifies_bool(monkeypatch: pytest.MonkeyPatch) -> None:
    """A bool YAML value is exported as "True"/"False" (Pydantic parses either casing).

    **Why this test is important:**
      - bool is an int subclass; without an explicit branch it would stringify via
        the int path inconsistently. Feature flags in YAML must reach settings as
        parseable booleans.

    **What it tests:**
      - apply_yaml_defaults({"flag": True}) exports SEARCH_FLAG="True".
    """
    from techai_webutils.foundation.config.bridge import apply_yaml_defaults

    monkeypatch.delenv("SEARCH_FLAG", raising=False)
    apply_yaml_defaults({"flag": True})
    assert os.environ["SEARCH_FLAG"] == "True"


def test_initialize_config_missing_base_yaml_is_graceful(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A config dir without base.yaml initializes cleanly (env-vars-only deployments).

    **Why this test is important:**
      - Docker/K8s deployments often ship no base.yaml and rely purely on env vars;
        the bridge must not raise, or every such deployment would crash at startup.

    **What it tests:**
      - initialize_config against an empty dir returns without error and marks init.
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    reset_config()
    with TemporaryDirectory() as empty:
        initialize_config(empty)  # no base.yaml present — must not raise
    reset_config()
