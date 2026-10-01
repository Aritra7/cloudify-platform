"""Tests for deterministic dry runs and redacted evidence."""

import json
from datetime import datetime, timezone
from pathlib import Path

import yaml
import pytest

from agents.base_agent import AgentResult, AgentStatus, Event, EventType
from agents.dry_run import execute_dry_run
from agents.base_agent import EventBus
from migration_orchestrator import build_effective_config, check_prerequisites
from utils.evidence import write_evidence


def demo_config() -> dict:
    return yaml.safe_load(Path("examples/demo-config.yaml").read_text())


def test_cli_overrides_are_preserved_without_mutating_template(tmp_path):
    template = demo_config()
    effective = build_effective_config(
        template,
        source_path=tmp_path,
        gcp_project="controlled-project",
        region="us-west1",
        mode="automated",
        dry_run=True,
    )

    assert effective["source"]["path"] == str(tmp_path.resolve())
    assert effective["gcp"]["project_id"] == "controlled-project"
    assert effective["gcp"]["region"] == "us-west1"
    assert effective["migration"]["mode"] == "automated"
    assert effective["migration"]["dry_run"] is True
    assert template["gcp"]["project_id"] == "replace-with-controlled-project"


def test_dry_run_has_no_external_prerequisites():
    assert check_prerequisites(dry_run=True) == (True, [])


@pytest.mark.asyncio
async def test_demo_fixture_dry_run_is_side_effect_free():
    config = demo_config()
    config["source"]["path"] = str(Path("examples/demo-app").resolve())
    event_bus = EventBus()

    result = await execute_dry_run(config, event_bus)

    assert result.status == AgentStatus.SUCCESS
    assert result.data["side_effects_executed"] is False
    assert result.data["analysis"]["backend"]["java_version"] == "21"
    assert result.data["analysis"]["frontend"]["build_tool"] == "vite"
    assert result.data["analysis"]["database"]["type"] == "h2"
    assert all(action["mutates_cloud"] for action in result.data["proposed_actions"])
    assert [event.event_type for event in event_bus.get_history()] == [
        EventType.AGENT_STARTED,
        EventType.AGENT_COMPLETED,
    ]


def test_evidence_recursively_redacts_secrets(tmp_path):
    now = datetime.now(timezone.utc)
    path = tmp_path / "evidence.json"
    result = AgentResult(
        status=AgentStatus.SUCCESS,
        data={"access_token": "result-secret", "summary": {"status": "ok"}},
    )
    events = [
        Event(
            EventType.AGENT_COMPLETED,
            "test",
            {"api_key": "event-secret", "status": "success"},
            timestamp=now,
        )
    ]
    write_evidence(
        path,
        config={
            "service_account_key": "/secret/key.json",
            "nested": {
                "password": "secret",
                "spring.datasource.password": "database-secret",
            },
            "max_tokens": 4096,
        },
        result=result,
        events=events,
        started_at=now,
        finished_at=now,
    )

    encoded = path.read_text()
    assert "result-secret" not in encoded
    assert "event-secret" not in encoded
    assert "database-secret" not in encoded
    assert "/secret/key.json" not in encoded
    assert json.loads(encoded)["result"]["data"]["access_token"] == "[REDACTED]"
    assert json.loads(encoded)["config"]["max_tokens"] == 4096


def test_evidence_normalizes_workspace_paths(tmp_path):
    now = datetime.now(timezone.utc)
    path = tmp_path / "evidence.json"
    result = AgentResult(
        status=AgentStatus.SUCCESS,
        data={"source": str(Path.cwd() / "examples/demo-app")},
    )
    write_evidence(
        path,
        config={"source": {"path": str(Path.cwd() / "examples/demo-app")}},
        result=result,
        events=[],
        started_at=now,
        finished_at=now,
    )

    evidence = json.loads(path.read_text())
    assert evidence["config"]["source"]["path"] == "./examples/demo-app"
    assert evidence["result"]["data"]["source"] == "./examples/demo-app"
