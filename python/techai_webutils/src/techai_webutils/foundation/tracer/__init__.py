"""Tracer implementations and builder.

Provides OTelTracerProvider, NullTracerProvider, and a builder factory
for config-driven tracer selection.
"""

from techai_webutils.foundation.tracer.builder import (
    TracerConfig,
    TracerKind,
    default_config,
    new_tracer_from_config,
)
from techai_webutils.foundation.tracer.null_tracer import NullTracerProvider
from techai_webutils.foundation.tracer.tracer import OTelTracerProvider, configure_tracer, new_tracer

__all__ = [
    "NullTracerProvider",
    "OTelTracerProvider",
    "TracerConfig",
    "TracerKind",
    "configure_tracer",
    "default_config",
    "new_tracer",
    "new_tracer_from_config",
]
