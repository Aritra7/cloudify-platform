"""Unit tests for the code-analysis agent and its deterministic tools."""

import json
from pathlib import Path

import pytest

from agents.base_agent import AgentStatus, EventBus
from agents.code_analyzer import CodeAnalyzerAgent
from agents.dedalus_tools import analyze_react_app, detect_database_type


@pytest.fixture
def event_bus():
    """Create event bus fixture."""
    return EventBus()


@pytest.fixture
def config():
    """Create configuration fixture."""
    return {
        "source": {
            "path": "/tmp/test-app",
            "backend": {
                "type": "spring-boot",
                "path": "backend",
            },
            "frontend": {
                "type": "react",
                "path": "frontend",
            },
        },
        "ai": {
            "model": "claude-opus-4-6",
            "temperature": 0.3,
        },
    }


@pytest.fixture
def code_analyzer(event_bus, config):
    """Create CodeAnalyzerAgent fixture."""
    return CodeAnalyzerAgent(
        event_bus=event_bus,
        config=config,
        dedalus_api_key="test-api-key",
    )


@pytest.mark.asyncio
async def test_analyzer_initialization(code_analyzer):
    """Test agent initialization."""
    assert code_analyzer.name == "CodeAnalyzer"
    assert code_analyzer.status.value == "idle"


@pytest.mark.asyncio
async def test_analyzer_invalid_source_path(code_analyzer, tmp_path):
    """Test analyzer with invalid source path."""
    code_analyzer.config["source"]["path"] = str(tmp_path / "missing")

    result = await code_analyzer.execute()

    assert result.status == AgentStatus.FAILED
    assert len(result.errors) > 0
    assert "does not exist" in result.errors[0]


@pytest.mark.asyncio
async def test_analyze_maven_backend(code_analyzer, tmp_path):
    """Analyze Maven metadata and Spring configuration through the agent."""
    backend_path = tmp_path / "backend"
    backend_path.mkdir()

    pom_xml = """<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
    <properties>
        <java.version>21</java.version>
    </properties>
    <parent>
        <groupId>org.springframework.boot</groupId>
        <artifactId>spring-boot-starter-parent</artifactId>
        <version>3.5.0</version>
    </parent>
    <dependencies>
        <dependency>
            <artifactId>spring-boot-starter-web</artifactId>
        </dependency>
    </dependencies>
</project>
"""
    (backend_path / "pom.xml").write_text(pom_xml)

    resources = backend_path / "src" / "main" / "resources"
    resources.mkdir(parents=True)
    (resources / "application.properties").write_text(
        "server.port=8080\n"
        "spring.datasource.url=jdbc:h2:mem:testdb\n"
        "spring.datasource.username=sa\n"
    )

    analysis = await code_analyzer._analyze_backend(str(backend_path))

    assert analysis["build_tool"] == "maven"
    assert analysis["java_version"] == "21"
    assert analysis["spring_boot_version"] == "3.5.0"
    assert "spring-boot-starter-web" in analysis["dependencies"]
    assert analysis["database_config"]["spring.datasource.url"] == "jdbc:h2:mem:testdb"
    assert analysis["server_config"]["server.port"] == "8080"


@pytest.mark.asyncio
async def test_detect_database_h2_memory():
    """Test H2 in-memory database analysis."""
    result = json.loads(await detect_database_type("jdbc:h2:mem:testdb"))

    assert result["type"] == "h2"
    assert result["mode"] == "in-memory"
    assert result["migration_recommended"] is True
    assert len(result["notes"]) > 0


@pytest.mark.asyncio
async def test_find_react_api_endpoints(tmp_path):
    """Test API endpoint detection in React code."""
    frontend_path = tmp_path / "frontend"
    src_path = frontend_path / "src"
    src_path.mkdir(parents=True)

    # Create test React file
    react_code = """
import axios from 'axios';

const API_URL = 'http://localhost:8080';

export const fetchData = () => {
    return axios.get(`${API_URL}/api/data`);
};
"""
    (src_path / "api.js").write_text(react_code)

    analysis = json.loads(await analyze_react_app(str(frontend_path)))

    assert "http://localhost:8080" in analysis["api_endpoints"]
