"""Run ollupi as an agent in Harbor (https://harborframework.com).

Harbor uploads ollupi's eval command, built for Linux by ../build.sh, into each task's container and runs the task
through it there; the container is the sandbox. The model is Harbor's -m with its provider prefix dropped, and the
rest comes from the host's environment, so no key is ever on a command line:

    OLLUPI_API_KEY   the provider's key (required)
    OLLUPI_API       openai (default, also for compatible providers), anthropic or gemini
    OLLUPI_BASE_URL  the provider's base URL
    OLLUPI_CONTEXT   the model's context window in tokens (default 128000)
    OLLUPI_OUTPUT    the most tokens one response writes (default 8192)

From the repository root:

    PYTHONPATH=eval/harbor harbor run -d terminal-bench@2.0 -m deepseek/deepseek-v4-pro \\
        -a ollupi_agent:Ollupi --n-tasks 10
"""

import os
import shlex
from pathlib import Path
from typing import override

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

BUILD_DIR = Path(__file__).resolve().parent.parent / "build"
BINARY = "/installed-agent/ollupi-eval"
ARCHES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
FORWARDED = ("OLLUPI_API", "OLLUPI_BASE_URL", "OLLUPI_CONTEXT", "OLLUPI_OUTPUT")


class Ollupi(BaseInstalledAgent):
    @staticmethod
    @override
    def name() -> str:
        return "ollupi"

    @override
    def get_version_command(self) -> str | None:
        return f"{BINARY} -version"

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        machine = (await environment.exec(command="uname -m")).stdout.strip()
        arch = ARCHES.get(machine)
        if arch is None:
            raise RuntimeError(f"ollupi: no build for a {machine} container")
        binary = BUILD_DIR / f"ollupi-eval-linux-{arch}"
        if not binary.is_file():
            raise RuntimeError(f"ollupi: {binary} is missing; run eval/build.sh first")
        await environment.upload_file(binary, BINARY)
        await self.exec_as_root(environment, command=f"chmod 755 {BINARY}")

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        pass

    @override
    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        if not self.model_name:
            raise ValueError("ollupi: pass the model with -m")
        key = os.environ.get("OLLUPI_API_KEY")
        if not key:
            raise ValueError("ollupi: set OLLUPI_API_KEY to the provider's key")
        env = {"OLLUPI_INSTRUCTION": instruction, "OLLUPI_API_KEY": key}
        flags = ["-model", self.model_name.split("/", 1)[-1], "-home", "/tmp/ollupi",
                 "-out", "/logs/agent/ollupi-items.json"]
        for name in FORWARDED:
            if value := os.environ.get(name):
                flags += [f"-{name.removeprefix('OLLUPI_').lower().replace('_', '-')}", value]
        command = " ".join(shlex.quote(part) for part in [BINARY, *flags])
        await self.exec_as_agent(environment, command=f"{command} 2>&1 | tee /logs/agent/ollupi.txt", env=env)
