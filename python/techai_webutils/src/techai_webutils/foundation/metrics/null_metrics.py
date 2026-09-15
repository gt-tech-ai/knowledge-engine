"""No-op metrics implementation.

All metric operations are silent no-ops. Useful for tests and environments
where Prometheus is not available.
"""

from __future__ import annotations

from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricGauge,
    MetricHistogram,
    MetricsProvider,
)


class NullMetricsProvider(MetricsProvider):
    """No-op metrics provider that creates null metric objects."""

    def counter(self, name: str, help_text: str, labels: list[str] | None = None) -> MetricCounter:  # noqa: ARG002
        """Return a no-op counter."""
        return _NullCounter()

    def histogram(
        self,
        name: str,  # noqa: ARG002
        help_text: str,  # noqa: ARG002
        labels: list[str] | None = None,  # noqa: ARG002
        buckets: list[float] | None = None,  # noqa: ARG002
    ) -> MetricHistogram:
        """Return a no-op histogram."""
        return _NullHistogram()

    def gauge(self, name: str, help_text: str, labels: list[str] | None = None) -> MetricGauge:  # noqa: ARG002
        """Return a no-op gauge."""
        return _NullGauge()


class _NullCounter(MetricCounter):
    """No-op counter."""

    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Discard the increment."""


class _NullHistogram(MetricHistogram):
    """No-op histogram."""

    def observe(self, value: float, **labels: str) -> None:
        """Discard the observation."""


class _NullGauge(MetricGauge):
    """No-op gauge."""

    def set(self, value: float, **labels: str) -> None:
        """Discard the set."""

    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Discard the increment."""

    def dec(self, value: float = 1.0, **labels: str) -> None:
        """Discard the decrement."""
