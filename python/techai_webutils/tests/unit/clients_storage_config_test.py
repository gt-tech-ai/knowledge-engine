"""Tests for S3Config.client_kwargs (aiobotocore connection normalization)."""

from __future__ import annotations

from techai_webutils.clients.storage.config import S3Config


class TestS3ConfigClientKwargs:
    """S3Config.client_kwargs normalizes empty endpoint/creds to None.

    Why this test is important:
      - The ingestion worker downloads documents through the S3 client. base.yaml defaults
        the endpoint to ``http://localhost:9000`` + ``minioadmin`` creds (dev MinIO);
        staging/prod blank them so the client uses the regional AWS endpoint + IRSA. Passing
        empty creds straight through would try to authenticate with ``""`` against real AWS.
        The empty -> None collapse is what routes a blanked config to the default (IRSA)
        credential chain. Mirrors SQSConfig.client_kwargs.

    What it tests:
      - Empty endpoint/creds map to None (regional endpoint + default credential chain).
      - Non-empty values (local MinIO) pass through unchanged; region always passes through.
    """

    def test_empty_endpoint_and_creds_collapse_to_none(self) -> None:
        config = S3Config(endpoint="", bucket="b", region="us-east-1", access_key="", secret_key="")
        kwargs = config.client_kwargs()
        assert kwargs["endpoint_url"] is None
        assert kwargs["aws_access_key_id"] is None
        assert kwargs["aws_secret_access_key"] is None
        assert kwargs["region_name"] == "us-east-1"

    def test_non_empty_values_pass_through(self) -> None:
        config = S3Config(
            endpoint="http://localhost:9000",
            bucket="b",
            region="us-east-1",
            access_key="minioadmin",
            secret_key="minioadmin",
        )
        kwargs = config.client_kwargs()
        assert kwargs["endpoint_url"] == "http://localhost:9000"
        assert kwargs["aws_access_key_id"] == "minioadmin"
        assert kwargs["aws_secret_access_key"] == "minioadmin"
