"""Unit tests for the Python PII redactor (a port of the Go ``RedactPII``) and its logger wiring."""

import io
import json
from pathlib import Path

from techai_webutils.foundation.logger import configure_logging, get_logger
from techai_webutils.foundation.logger.redact import redact_pii

_VECTORS = Path(__file__).resolve().parents[4] / "testdata" / "redact_vectors.json"


def test_redact_pii_matches_go_vectors():
    """Test that ``redact_pii`` produces Go's exact output for every shared vector.

    **Why this test is important:**
      - Go and Python both emit logs and span events from the same requests; a pattern that redacts
        in one language and leaks in the other is a compliance gap no single-language test sees.

    **What it tests:**
      - for each case in ``testdata/redact_vectors.json`` (also read by the Go suite),
        ``redact_pii(input) == want``
    """
    vectors = json.loads(_VECTORS.read_text(encoding="utf-8"))

    assert vectors
    for vector in vectors:
        assert redact_pii(vector["input"]) == vector["want"], vector["name"]


def test_logger_applies_redaction_when_flag_set():
    """Test that ``configure_logging(redact_pii=True)`` redacts the message and string fields.

    **Why this test is important:**
      - ``logging_redact_pii`` is the setting operators rely on to keep PII out of the log store;
        it must actually change the emitted line.

    **What it tests:**
      - with the flag set, the message and a string field are redacted while ``trace``-style
        structural fields and non-string values are untouched
      - with the flag unset, the same line is emitted verbatim
    """
    redacted_stream = io.StringIO()
    configure_logging(level="INFO", stream=redacted_stream, redact_pii=True)
    get_logger("t").info("mail john@example.com", caller="555-123-4567", attempts=3)

    plain_stream = io.StringIO()
    configure_logging(level="INFO", stream=plain_stream)
    get_logger("t").info("mail john@example.com", caller="555-123-4567", attempts=3)

    redacted = json.loads(redacted_stream.getvalue().strip().splitlines()[-1])
    plain = json.loads(plain_stream.getvalue().strip().splitlines()[-1])
    assert redacted["message"] == "mail [REDACTED]"
    assert redacted["caller"] == "[REDACTED]"
    assert redacted["attempts"] == 3
    assert plain["message"] == "mail john@example.com"
    assert plain["caller"] == "555-123-4567"
