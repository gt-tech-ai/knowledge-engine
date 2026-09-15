"""Pipelines layer - stateless data transformation.

Pipelines model sequential operations where each stage consumes the output of the
prior stage. Unlike workflows, pipelines do not coordinate state or orchestrate
multiple services. They are pure transformations: Input → Processing → Output.

Pipelines implements the sixth layer in the nine-layer architecture model:
core → foundation → clients → repos → services → pipelines → workflows → controllers → entrypoints
"""

from techai_webutils.pipelines.base import BaseAsyncPipeline, BasePipeline
from techai_webutils.pipelines.decorators import (
    LoggingPipelineDecorator,
    MetricsPipelineDecorator,
    PipelineBuilder,
)

__all__ = [
    "BaseAsyncPipeline",
    "BasePipeline",
    "LoggingPipelineDecorator",
    "MetricsPipelineDecorator",
    "PipelineBuilder",
]
