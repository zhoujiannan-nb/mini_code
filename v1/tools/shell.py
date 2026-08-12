"""Shell command execution tool."""

import asyncio
import os
import re
import sys
from typing import Any

from loguru import logger

from .base import Tool


class ExecTool(Tool):
    """Execute shell commands and return output."""

    _MAX_TIMEOUT = 600
    _MAX_OUTPUT = 10_000

    def __init__(self, timeout: int = 60, working_dir: str | None = None):
        self.timeout = timeout
        self.working_dir = working_dir

    @property
    def name(self) -> str:
        return "exec"

    @property
    def description(self) -> str:
        return "Execute a shell command and return stdout/stderr plus exit code."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "command": {
                    "type": "string",
                    "description": "Shell command to execute",
                },
                "working_dir": {
                    "type": "string",
                    "description": "Working directory (default: current dir)",
                },
                "timeout": {
                    "type": "integer",
                    "description": "Timeout in seconds (default 60, max 600)",
                    "minimum": 1,
                    "maximum": 600,
                },
            },
            "required": ["command"],
        }

    async def execute(
        self, command: str, working_dir: str | None = None,
        timeout: int | None = None, **kwargs: Any,
    ) -> str:
        cwd = working_dir if working_dir is not None else (self.working_dir or os.getcwd())

        # Validate working directory
        if working_dir is not None and not os.path.exists(cwd):
            logger.warning(f"Working directory not found: {cwd}, falling back to current dir")
            cwd = os.getcwd()

        # Safety guard
        guard_error = self._guard_command(command)
        if guard_error:
            return guard_error

        effective_timeout = min(timeout or self.timeout, self._MAX_TIMEOUT)

        try:
            process = await asyncio.create_subprocess_shell(
                command,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=cwd,
            )

            try:
                stdout, stderr = await asyncio.wait_for(
                    process.communicate(),
                    timeout=effective_timeout,
                )
            except asyncio.TimeoutError:
                process.kill()
                try:
                    await asyncio.wait_for(process.wait(), timeout=5.0)
                except asyncio.TimeoutError:
                    pass
                finally:
                    if sys.platform != "win32":
                        try:
                            os.waitpid(process.pid, os.WNOHANG)
                        except (ProcessLookupError, ChildProcessError):
                            pass
                return f"Error: Command timed out after {effective_timeout}s"

            # Build output
            parts = []
            if stdout:
                parts.append(stdout.decode("utf-8", errors="replace"))
            if stderr:
                stderr_text = stderr.decode("utf-8", errors="replace").strip()
                if stderr_text:
                    parts.append(f"STDERR:\n{stderr_text}")
            parts.append(f"\nExit code: {process.returncode}")

            result = "\n".join(parts) if parts else "(no output)"

            # Truncate large output preserving head + tail
            if len(result) > self._MAX_OUTPUT:
                half = self._MAX_OUTPUT // 2
                result = (
                    result[:half]
                    + f"\n\n... ({len(result) - self._MAX_OUTPUT:,} chars truncated) ...\n\n"
                    + result[-half:]
                )

            return result

        except Exception as e:
            return f"Error executing command: {e}"

    def _guard_command(self, command: str) -> str | None:
        """Block clearly destructive commands."""
        lower = command.strip().lower()
        dangerous = [
            r"\brm\s+-[rf]{1,2}\b",
            r"\bdel\s+/[fq]\b",
            r"\brmdir\s+/s\b",
            r"(?:^|[;&|]\s*)format\b",
            r"\b(mkfs|diskpart)\b",
            r"\bdd\s+if=",
            r">\s*/dev/sd",
            r"\b(shutdown|reboot|poweroff)\b",
            r":\(\)\s*\{.*\};\s*:",
        ]
        for pattern in dangerous:
            if re.search(pattern, lower):
                return "Error: Command blocked — dangerous pattern detected"
        return None
