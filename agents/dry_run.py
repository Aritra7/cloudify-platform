"""Deterministic, side-effect-free migration preview."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .base_agent import AgentResult, AgentStatus, Event, EventBus, EventType
from .dedalus_tools import (
    analyze_react_app,
    analyze_spring_properties,
    detect_database_type,
    extract_api_endpoints,
    scan_gradle_build,
    scan_maven_pom,
)


async def execute_dry_run(config: dict[str, Any], event_bus: EventBus) -> AgentResult:
    """Analyze a source tree and describe actions without external calls."""
    source = Path(config["source"]["path"])
    backend = source / config["source"]["backend"]["path"]
    frontend = source / config["source"]["frontend"]["path"]
    errors: list[str] = []
    if not source.is_dir():
        errors.append(f"Source path does not exist: {source}")
    if not backend.is_dir():
        errors.append(f"Backend path does not exist: {backend}")
    if not frontend.is_dir():
        errors.append(f"Frontend path does not exist: {frontend}")
    if errors:
        return AgentResult(status=AgentStatus.FAILED, data={}, errors=errors)

    await event_bus.publish(
        Event(EventType.AGENT_STARTED, "DryRun", {"agent": "DryRun"})
    )
    if (backend / "pom.xml").is_file():
        backend_build = json.loads(await scan_maven_pom(str(backend)))
        backend_build["build_tool"] = "maven"
    elif (backend / "build.gradle").is_file():
        backend_build = json.loads(await scan_gradle_build(str(backend)))
        backend_build["build_tool"] = "gradle"
    else:
        backend_build = {"build_tool": None}
        errors.append("Backend has neither pom.xml nor build.gradle")

    properties = json.loads(await analyze_spring_properties(str(backend)))
    controllers = json.loads(await extract_api_endpoints(str(backend)))
    frontend_analysis = json.loads(await analyze_react_app(str(frontend)))
    database_url = properties.get("database_config", {}).get(
        "spring.datasource.url", ""
    )
    database = (
        json.loads(await detect_database_type(database_url))
        if database_url
        else {"type": "unknown"}
    )
    gcp = config["gcp"]
    proposed_actions = [
        {"phase": "infrastructure", "action": "enable APIs", "mutates_cloud": True},
        {
            "phase": "infrastructure",
            "action": "ensure Artifact Registry repository",
            "target": gcp["artifact_registry"]["repository_name"],
            "mutates_cloud": True,
        },
        {
            "phase": "backend",
            "action": "build image and deploy Cloud Run service",
            "target": gcp["backend"]["service_name"],
            "mutates_cloud": True,
        },
        {
            "phase": "frontend",
            "action": "build and deploy Firebase site",
            "target": gcp["frontend"]["site_name"],
            "mutates_cloud": True,
        },
    ]
    if gcp["database"]["strategy"] == "migrate-to-cloud-sql":
        proposed_actions.append(
            {
                "phase": "database",
                "action": "create Cloud SQL instance and database",
                "target": gcp["database"]["cloud_sql"]["instance_name"],
                "mutates_cloud": True,
            }
        )
    data = {
        "summary": {
            "migration_status": "previewed" if not errors else "failed",
            "project_id": gcp["project_id"],
            "region": gcp["region"],
            "source": str(source),
        },
        "analysis": {
            "backend": {**backend_build, **properties, "controllers": controllers},
            "frontend": frontend_analysis,
            "database": database,
        },
        "proposed_actions": proposed_actions,
        "side_effects_executed": False,
    }
    status = AgentStatus.FAILED if errors else AgentStatus.SUCCESS
    await event_bus.publish(
        Event(
            EventType.AGENT_COMPLETED if not errors else EventType.AGENT_FAILED,
            "DryRun",
            {"agent": "DryRun", "status": status.value, "side_effects_executed": False},
        )
    )
    return AgentResult(status=status, data=data, errors=errors)
