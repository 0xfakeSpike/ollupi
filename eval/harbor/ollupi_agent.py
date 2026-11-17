"""Harbor agent that runs ollupi's eval command inside each task's container; see eval/README.md."""

import os
from pathlib import Path
from typing import override

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

BUILD_DIR = Path(__file__).resolve().parent.parent / "build"
BINARY = "/installed-agent/ollupi-eval"
ARCHES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}


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
        binary = BUILD_DIR / f"ollupi-eval-linux-{ARCHES.get(machine, machine)}"
        if not binary.is_file():
            raise RuntimeError(f"ollupi: {binary} is missing; run eval/build.sh for a {machine} container")
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
        env = {name: value for name, value in os.environ.items() if name.startswith("OLLUPI_")}
        env |= {"OLLUPI_INSTRUCTION": instruction, "OLLUPI_MODEL": self.model_name.split("/", 1)[-1]}
        await self.exec_as_agent(
            environment,
            command=f"{BINARY} -out /logs/agent/ollupi-items.json 2>&1 | tee /logs/agent/ollupi.txt",
            env=env,
        )
