"""YAML-to-environment variable bridge for Pydantic Settings integration.

This module bridges the gap between YAML configuration files and Pydantic Settings.
It loads the merged YAML configuration (``base.yaml`` + ``{env}.yaml`` overlay +
``secrets.yaml``) and exports every scalar leaf as a ``SEARCH_*`` environment variable,
allowing Pydantic Settings to consume them via its ``env_prefix="SEARCH_"`` behavior.

Why this design:
  - Pydantic Settings reads from environment variables by default.
  - YAML provides the hierarchical, environment-layered config the Go services also use.
  - Exporting the whole tree as ``SEARCH_*`` env defaults mirrors Viper's ``AutomaticEnv`` +
    ``SetEnvPrefix`` in Go: the YAML supplies defaults, real env vars still win.
  - Environment variables always take precedence (12-factor app principle).
  - Idempotency ensures safe multi-call scenarios (tests, multi-module imports).

The env-var name for a YAML path is the dotted path upper-snake-cased with a ``SEARCH_``
prefix (``retrieval.llm.kind`` -> ``SEARCH_RETRIEVAL_LLM_KIND``), matching the Pydantic
field names exactly. This generic flattening replaced a hand-maintained dotted-path map
whose omissions (e.g. ``retrieval.llm.*``) silently dropped whole config sections, so a
service fell back to its stub/localhost defaults instead of honoring the overlay.

Usage:
    from techai_webutils.foundation.config.bridge import initialize_config

    # Early in application startup (before creating Settings instances)
    initialize_config("/path/to/config")

    # Now Pydantic Settings will read SEARCH_* env vars set from YAML
    settings = DatabaseSettings()
"""

import logging
import os
import threading
from pathlib import Path
from typing import Any

from techai_webutils.foundation.config.loader import load_config

logger = logging.getLogger(__name__)

# Sentinel flag + lock ensuring initialize_config() loads the YAML exactly once, thread-safely
# (audit #30 — the former unlocked check-then-set was safe only if init preceded any thread spawn).
_initialized = False
_init_lock = threading.Lock()


def _flatten_config(config: dict[str, Any], prefix: str = "SEARCH") -> list[tuple[str, str]]:
    """Flatten a nested config dict into ``(SEARCH_<UPPER_SNAKE>, value)`` pairs.

    Nested dicts extend the prefix (``a.b.c`` -> ``SEARCH_A_B_C``), which matches the
    Pydantic Settings field names (``env_prefix="SEARCH_"``). Scalar leaves (str/int/
    float/bool) are stringified; ``None`` and list/sequence leaves are skipped — they
    are not representable as a single env-var override through this bridge, and no
    ``SEARCH_*`` settings field reads one. Returns a flat list (not a generator) so the
    traversal is eager and easy to reason about at the call site.
    """
    items: list[tuple[str, str]] = []
    for key, value in config.items():
        env_key = f"{prefix}_{key.upper()}"
        if isinstance(value, dict):
            items.extend(_flatten_config(value, env_key))
        elif isinstance(value, bool):
            # bool is an int subclass — handle before the int/float/str branch so it
            # stringifies as "True"/"False" (Pydantic parses either casing).
            items.append((env_key, str(value)))
        elif isinstance(value, (str, int, float)):
            items.append((env_key, str(value)))
        # None and lists are intentionally skipped.
    return items


def apply_yaml_defaults(config: dict[str, Any]) -> None:
    """Export merged YAML values as ``SEARCH_*`` environment variables.

    Every scalar leaf of the config tree becomes a ``SEARCH_<UPPER_SNAKE>`` env var, but
    only when that var is NOT already set — so real environment variables (e.g. secrets
    injected by the deployment) take precedence over YAML values (12-factor principle).

    Args:
        config: Merged configuration dictionary loaded from YAML.

    """
    for env_var, value in _flatten_config(config):
        # Skip if env var already set (env vars take precedence over YAML)
        if env_var in os.environ:
            continue
        os.environ[env_var] = value


def _resolve_environment() -> str:
    """Resolve the overlay environment name for ``{env}.yaml`` selection.

    Prefers ``SEARCH_ENV`` (the Go convention), then ``APP_ENV`` (set by the K8s
    deployments), defaulting to ``dev`` so local runs load ``base.yaml`` + ``dev.yaml``.
    """
    return os.environ.get("SEARCH_ENV") or os.environ.get("APP_ENV") or "dev"


def initialize_config(config_dir: str | Path = ".") -> None:
    """Initialize configuration by loading the merged YAML and setting env variables.

    Loads ``base.yaml`` + the ``{env}.yaml`` overlay (selected by ``SEARCH_ENV``/``APP_ENV``)
    + ``secrets.yaml``, then exports every scalar leaf as a ``SEARCH_*`` env var (without
    overriding vars already set). This is idempotent — the first call loads YAML and sets
    env vars; subsequent calls are no-ops.

    Args:
        config_dir: Directory containing base.yaml (default: current directory).

    """
    # Idempotency + thread-safety: a fast-path read, then a double-check under the lock, so concurrent
    # initialize_config() calls (multi-module imports, off-thread wiring) load the YAML exactly once.
    # (Only reads _initialized here; the write happens in _load_and_apply_config under the lock.)
    if _initialized:
        return
    with _init_lock:
        if _initialized:
            return
        _load_and_apply_config(config_dir)


def _load_and_apply_config(config_dir: str | Path) -> None:
    """Load the merged YAML + export ``SEARCH_*`` env defaults, then set the initialized sentinel.

    Runs under ``_init_lock`` (called only from ``initialize_config``); every early return still marks
    the config initialized so a missing/broken config is not retried on every call.
    """
    global _initialized  # noqa: PLW0603

    config_path = Path(config_dir)

    # Graceful fallback for missing config directory (Docker/K8s deployments)
    if not config_path.exists():
        logger.warning(
            "Config directory not found: %s. Proceeding with environment variables only.",
            config_path,
        )
        _initialized = True
        return

    config_file = config_path / "base.yaml"
    if not config_file.exists():
        logger.warning(
            "Config file not found: %s. Proceeding with environment variables only.",
            config_file,
        )
        _initialized = True
        return

    # Load and deep-merge base.yaml + {env}.yaml + secrets.yaml (env vars still win below).
    environment = _resolve_environment()
    try:
        config = load_config(str(config_dir), environment=environment)
    except Exception:
        logger.warning("Failed to load config from %s", config_dir, exc_info=True)
        _initialized = True
        return

    # Apply YAML values to env vars (respecting existing env vars)
    apply_yaml_defaults(config)

    _initialized = True


def reset_config() -> None:
    """Reset the initialization sentinel flag.

    This is for testing only. It allows tests to call initialize_config()
    multiple times with different configurations.
    """
    global _initialized  # noqa: PLW0603
    _initialized = False
