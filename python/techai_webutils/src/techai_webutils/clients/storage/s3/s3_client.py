"""S3-compatible storage client using aiobotocore."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Self

import aiobotocore.session  # type: ignore[import-untyped]
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject

_DOWNLOAD_CHUNK_SIZE = 1024 * 1024
"""Read size for streamed downloads, in bytes (1 MiB) — keeps peak RAM O(chunk), not O(file)."""

_MULTIPART_COPY_THRESHOLD = 100 * 1024 * 1024
"""Object size, in bytes, above which a server-side copy switches to multipart UploadPartCopy (100 MiB)."""

_COPY_PART_SIZE = 100 * 1024 * 1024
"""Byte range each UploadPartCopy part covers (100 MiB; above the 5 MiB S3 minimum)."""

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.clients.storage.config import S3Config


class S3StorageClient(StorageClient):
    """Object storage operations backed by S3 via aiobotocore.

    Usage::

        async with S3StorageClient(config) as storage:
            await storage.upload("bucket", "key", b"data", "text/plain")
    """

    def __init__(self, config: S3Config) -> None:
        """Store the S3 connection config; the client is created on context entry."""
        self._config = config
        self._client: object | None = None
        self._session: object | None = None

    async def __aenter__(self) -> Self:
        """Open the underlying aiobotocore S3 client and return self."""
        self._session = aiobotocore.session.get_session()
        self._client = await self._session.create_client(  # type: ignore[union-attr]
            "s3",
            **self._config.client_kwargs(),
        ).__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the underlying aiobotocore S3 client on context exit."""
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)  # type: ignore[union-attr]

    async def upload(self, bucket: str, key: str, body: bytes, content_type: str) -> None:
        """Upload an object to S3."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        await self._client.put_object(  # type: ignore[union-attr]
            Bucket=bucket,
            Key=key,
            Body=body,
            ContentType=content_type,
        )

    async def download(self, bucket: str, key: str) -> bytes:
        """Download an object from S3."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        resp = await self._client.get_object(Bucket=bucket, Key=key)  # type: ignore[union-attr]
        async with resp["Body"] as stream:
            return await stream.read()  # type: ignore[no-any-return]

    async def delete(self, bucket: str, key: str) -> None:
        """Delete an object from S3."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        await self._client.delete_object(Bucket=bucket, Key=key)  # type: ignore[union-attr]

    async def copy(self, bucket: str, src_key: str, dst_key: str) -> None:
        """Copy an object within the bucket server-side (no data through this process).

        A small object is copied with a single ``CopyObject``; a large one (over
        ``_MULTIPART_COPY_THRESHOLD``) uses multipart ``UploadPartCopy`` — AWS requires it above 5 GiB
        and it is far more reliable than a single multi-GB copy (which is slow + timeout-prone). The
        source size drives the choice; a failed multipart copy aborts the upload so no partial object
        is left behind.
        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        head = await self._client.head_object(Bucket=bucket, Key=src_key)  # type: ignore[union-attr]
        size = int(head["ContentLength"])
        source = {"Bucket": bucket, "Key": src_key}
        if size <= _MULTIPART_COPY_THRESHOLD:
            await self._client.copy_object(Bucket=bucket, CopySource=source, Key=dst_key)  # type: ignore[union-attr]
            return
        await self._multipart_copy(bucket, source, dst_key, size)

    async def _multipart_copy(self, bucket: str, source: dict[str, str], dst_key: str, size: int) -> None:
        """Server-side copy a large object via multipart UploadPartCopy, aborting on any failure."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        created = await self._client.create_multipart_upload(Bucket=bucket, Key=dst_key)  # type: ignore[union-attr]
        upload_id = created["UploadId"]
        try:
            parts: list[dict[str, object]] = []
            for part_number, start in enumerate(range(0, size, _COPY_PART_SIZE), start=1):
                end = min(start + _COPY_PART_SIZE, size) - 1
                part = await self._client.upload_part_copy(  # type: ignore[union-attr]
                    Bucket=bucket,
                    Key=dst_key,
                    UploadId=upload_id,
                    PartNumber=part_number,
                    CopySource=source,
                    CopySourceRange=f"bytes={start}-{end}",
                )
                parts.append({"ETag": part["CopyPartResult"]["ETag"], "PartNumber": part_number})
            await self._client.complete_multipart_upload(  # type: ignore[union-attr]
                Bucket=bucket,
                Key=dst_key,
                UploadId=upload_id,
                MultipartUpload={"Parts": parts},
            )
        except Exception:
            await self._client.abort_multipart_upload(  # type: ignore[union-attr]
                Bucket=bucket, Key=dst_key, UploadId=upload_id
            )
            raise

    async def exists(self, bucket: str, key: str) -> bool:
        """Check if an object exists in S3."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        try:
            await self._client.head_object(Bucket=bucket, Key=key)  # type: ignore[union-attr]
            return True
        except Exception:
            return False

    async def presign_url(self, bucket: str, key: str, expiry_seconds: int) -> str:
        """Generate a pre-signed URL for temporary access."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        url: str = await self._client.generate_presigned_url(  # type: ignore[union-attr]
            "get_object",
            Params={"Bucket": bucket, "Key": key},
            ExpiresIn=expiry_seconds,
        )
        return url

    async def list_objects(self, bucket: str, prefix: str) -> list[StorageObject]:
        """List objects in a bucket with the given prefix."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        resp = await self._client.list_objects_v2(  # type: ignore[union-attr]
            Bucket=bucket, Prefix=prefix
        )
        objects: list[StorageObject] = [
            StorageObject(
                key=item["Key"],
                size=item["Size"],
                content_type="",
                last_modified=str(item.get("LastModified", "")),
                etag=item.get("ETag", ""),
            )
            for item in resp.get("Contents", [])
        ]
        return objects

    async def stat(self, bucket: str, key: str) -> StorageObject:
        """HEAD an object and map its metadata; ``size`` is the real ``ContentLength``."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        resp = await self._client.head_object(Bucket=bucket, Key=key)  # type: ignore[union-attr]
        return StorageObject(
            key=key,
            size=int(resp["ContentLength"]),
            content_type=resp.get("ContentType", ""),
            last_modified=str(resp.get("LastModified", "")),
            etag=resp.get("ETag", ""),
        )

    async def download_to_path(self, bucket: str, key: str, path: str) -> None:
        """Stream an object's body to ``path`` chunk-by-chunk (never materialized as one bytes)."""
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise RuntimeError(msg)
        resp = await self._client.get_object(Bucket=bucket, Key=key)  # type: ignore[union-attr]
        # Offload the blocking file open/write/close to a thread so the streamed download never
        # blocks the event loop (writes are sequential — awaited one at a time — so the file
        # handle is only ever touched by one pool thread at a time).
        f = await asyncio.to_thread(open, path, "wb")
        # aiobotocore's StreamingBody is a wrapt.ObjectProxy over aiohttp's ClientResponse. Its
        # ``__aenter__`` returns the *wrapped* ClientResponse (whose read() takes no size arg), so we
        # must read off the StreamingBody proxy itself — it exposes read(amt) (validating content
        # length), the async analogue of botocore's sync read; the proxy has no iter_chunks(). Keep the
        # proxy reference and use ``async with`` only to close the underlying response on the way out.
        body = resp["Body"]
        try:
            async with body:
                while True:
                    chunk = await body.read(_DOWNLOAD_CHUNK_SIZE)
                    if not chunk:
                        break
                    await asyncio.to_thread(f.write, chunk)
        finally:
            await asyncio.to_thread(f.close)
