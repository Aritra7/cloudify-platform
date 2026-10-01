"""Migration evidence generation with recursive secret redaction."""

from __future__ import annotations

import json
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


SENSITIVE_KEY_SUFFIXES = ("credential", "key", "password", "secret", "token")


def redact(value: Any, key: str = "") -> Any:
    """Return a JSON-safe copy with values under sensitive keys removed."""
    normalized = re.sub(r"[^a-z0-9]+", "_", key.lower()).strip("_")
    if normalized in SENSITIVE_KEY_SUFFIXES or normalized.endswith(
        tuple(f"_{suffix}" for suffix in SENSITIVE_KEY_SUFFIXES)
    ):
        return "[REDACTED]" if value not in (None, "") else value
    if isinstance(value, dict):
        return {
            str(child_key): redact(child, str(child_key))
            for child_key, child in value.items()
        }
    if isinstance(value, (list, tuple)):
        return [redact(child) for child in value]
    if isinstance(value, datetime):
        return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
    if isinstance(value, Path):
        return str(value)
    if isinstance(value, str):
        workspace = str(Path.cwd().resolve())
        if value == workspace:
            return "."
        if value.startswith(workspace + "/"):
            return "./" + value[len(workspace) + 1 :]
    return value


def repository_revision() -> str | None:
    """Read the checked-out revision without invoking a shell."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            check=True,
            capture_output=True,
            text=True,
            timeout=5,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    return result.stdout.strip() or None


def write_evidence(
    path: Path,
    *,
    config: dict[str, Any],
    result: Any,
    events: list[Any],
    started_at: datetime,
    finished_at: datetime,
) -> None:
    """Write one self-describing, redacted evidence document atomically."""
    event_records = [
        {
            "event_id": event.event_id,
            "type": event.event_type.value,
            "source": event.source_agent,
            "timestamp": event.timestamp.astimezone(timezone.utc)
            .isoformat()
            .replace("+00:00", "Z"),
            "data": redact(event.data),
        }
        for event in events
    ]
    document = {
        "schema_version": "1.0",
        "generated_at": finished_at.astimezone(timezone.utc)
        .isoformat()
        .replace("+00:00", "Z"),
        "repository_revision": repository_revision(),
        "run": {
            "mode": "dry-run" if config.get("migration", {}).get("dry_run") else "live",
            "started_at": started_at.astimezone(timezone.utc)
            .isoformat()
            .replace("+00:00", "Z"),
            "finished_at": finished_at.astimezone(timezone.utc)
            .isoformat()
            .replace("+00:00", "Z"),
            "duration_seconds": round((finished_at - started_at).total_seconds(), 3),
            "status": result.status.value,
        },
        "config": redact(config),
        "result": {
            "data": redact(result.data),
            "errors": redact(result.errors),
            "warnings": redact(result.warnings),
            "models_used": list(result.models_used),
            "tools_called": list(result.tools_called),
        },
        "events": event_records,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)
