**Development:**
[![uv](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/astral-sh/uv/main/assets/badge/v0.json)](https://github.com/astral-sh/uv)
[![ruff](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/astral-sh/ruff/main/assets/badge/v2.json)](https://github.com/astral-sh/ruff)
[![pytest](https://img.shields.io/badge/pytest-enabled-brightgreen)](https://docs.pytest.org/)
[![CI](https://github.com/gt-tech-ai/knowledge-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/gt-tech-ai/knowledge-engine/actions/workflows/ci.yml)
[![GitHub commit activity](https://img.shields.io/github/commit-activity/y/gt-tech-ai/knowledge-engine?color=dark-green)](https://github.com/gt-tech-ai/knowledge-engine/commits/main/)

<!-- Content above this delimiter will be copied to the generated README.md file. DO NOT REMOVE THIS COMMENT, as it will cause regeneration to fail. -->

## Contents

- [Overview](#overview)

## Overview

`techai_webutils` is the Python mirror of the knowledge-engine's Go substrate. It follows the same
layers (`core` → `foundation` (+ `execution`) → `clients` → `repos` → `services` → `pipelines` →
`workflows` → `controllers`) and the same rules, described in the repository's
[ARCHITECTURE.md](../../ARCHITECTURE.md). Every swappable component is a `Kind` + a config +
a `new_<type>_from_config` factory, so switching a backend (memory → S3, stub → Bedrock,
asyncio → Ray) is a configuration change.

### How to use `techai_webutils`

```bash
uv add techai-webutils            # or: pip install techai-webutils
uv add "techai-webutils[postgres]"  # optional backends: ray, postgres, parsing, langdetect
```

Build a component from its config at your composition root and inject it:

```python
from techai_webutils.clients.storage import S3Config, StorageConfig, StorageKind, new_storage_from_config

config = StorageConfig(
    kind=StorageKind.MEMORY,  # StorageKind.S3 in a deployment: same code, different config
    s3=S3Config(endpoint="", bucket="documents", region="us-east-1"),
)
storage = new_storage_from_config(config)
```

Optional extras load only when their backend is selected, so the default install stays light.

<!-- Content below this delimiter will be copied to the generated README.md file. DO NOT REMOVE THIS COMMENT, as it will cause regeneration to fail. -->
