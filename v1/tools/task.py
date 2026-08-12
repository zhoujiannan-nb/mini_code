"""Task tool — 主 Agent 派发子任务给子 Agent。

仅限 mode="primary" 的主 Agent 使用。
子 Agent 在独立会话中前台运行，主 Agent 等待结果返回。
"""

from typing import Any

from loguru import logger

from agent.agent import list_all_agents
from .base import Tool


class ToolConfig:
    """子代理工具配置"""

    def __init__(self, name: str, description: str):
        self.name = name
        self.description = description


# 内置子代理列表
_BUILTIN_SUBAGENTS: list[ToolConfig] = [
    ToolConfig(
        name="explore",
        description=(
            "Fast read-only agent for exploring codebases. "
            "Use when you need to quickly find files by patterns, "
            "search code for keywords, or answer questions about the codebase. "
            "Has read_file, list_dir, exec (read-only), skills tools."
        ),
    ),
    ToolConfig(
        name="generate",
        description=(
            "API documentation generator sub-agent. "
            "Use this when you need to generate structured API docs from source code. "
            "The agent scans controllers/routes/models, extracts API info, "
            "and uses the api_manager tool to write results (incremental merge). "
            "Provide the project identifier, branch, and source code path in the goal."
        ),
    ),
]


def _get_all_subagents() -> list[ToolConfig]:
    """获取所有可用子代理（内置 + 自定义，动态合并）。"""
    all_agents = list_all_agents()
    builtin_names = {a.name for a in _BUILTIN_SUBAGENTS}
    result = list(_BUILTIN_SUBAGENTS)
    for name, desc in all_agents.items():
        if name not in builtin_names:
            result.append(ToolConfig(name=name, description=desc))
    return result


def _get_all_subagent_names() -> list[str]:
    """获取所有子代理名称（用于 task tool 参数校验）。"""
    from agent.agent import get_agent_config
    all_agents = list_all_agents()
    # 只返回 mode=subagent 的 agent（排除 primary 主 agent）
    names = []
    for name in all_agents:
        try:
            cfg = get_agent_config(name)
            if cfg.mode == "subagent":
                names.append(name)
        except ValueError:
            pass
    return names


def _build_agent_list_text() -> str:
    """构建子代理列表描述文本（嵌入到工具 description 中）。"""
    lines = []
    for agent in _get_all_subagents():
        lines.append(f"  - {agent.name}: {agent.description}")
    return "\n".join(lines)


# ============================================================================
#  TaskTool
# ============================================================================

class TaskTool(Tool):
    """主 Agent 专用的子任务派发工具。

    调用流程：
      1. 创建子会话（parent_id = 当前主会话 ID）
      2. 调用 session.prompt() 执行任务
      3. 前台等待子 Agent 完成
      4. 将子 Agent 的最终输出返回给主 Agent
    """

    def __init__(self):
        self._session_manager = None
        self._parent_session_id: str = ""
        self._work_dir: str = ""
        self._allow_dispatch: bool = False  # 仅根会话启用时设为 True

    # ── 会话上下文注入（由 PromptBuilder 在 prompt() 前设置） ──

    def set_session_context(
        self,
        session_manager: Any,
        parent_session_id: str,
        work_dir: str,
    ):
        """设置会话上下文。

        Args:
            session_manager: SessionManager 实例
            parent_session_id: 当前主会话 ID（作为父会话 ID）
            work_dir: 工作目录
        """
        self._session_manager = session_manager
        self._parent_session_id = parent_session_id
        self._work_dir = work_dir
        self._allow_dispatch = True

    # ── Tool 接口实现 ──────────────────────────────────────

    @property
    def name(self) -> str:
        return "task"

    @property
    def description(self) -> str:
        agent_list = _build_agent_list_text()
        return (
            "Dispatch a sub-task to a dedicated sub-agent running in its own session.\n\n"
            "Available sub-agents:\n"
            f"{agent_list}\n\n"
            "Usage: specify `sub_agent` name and a clear `goal` description. "
            "The sub-agent runs in a foreground session (blocking) and returns its "
            "final output when done."
        )

    @property
    def parameters(self) -> dict[str, Any]:
        agent_names = _get_all_subagent_names()
        return {
            "type": "object",
            "properties": {
                "sub_agent": {
                    "type": "string",
                    "description": (
                        f"Name of the sub-agent to delegate to. "
                        f"Available: {', '.join(agent_names)}"
                    ),
                    "enum": agent_names,
                },
                "goal": {
                    "type": "string",
                    "description": (
                        "Detailed task description for the sub-agent. "
                        "Include all context, constraints, and expected output format."
                    ),
                },
            },
            "required": ["sub_agent", "goal"],
        }

    async def execute(
        self,
        sub_agent: str = "",
        goal: str = "",
        **kwargs: Any,
    ) -> str:
        """执行子任务。

        1. 校验子代理名称
        2. 创建子会话（parent_id = 当前主会话 ID）
        3. 调用 session.prompt() 前台等待
        4. 返回子代理输出
        """
        # ── 前置校验 ──
        if not sub_agent:
            return "Error: missing required parameter 'sub_agent'"
        if not goal:
            return "Error: missing required parameter 'goal'"

        # ── 防御性检查：子 Agent 禁止派发子任务 ──
        if not self._allow_dispatch:
            logger.warning(
                "[TaskTool] 拒绝派发：当前会话无权使用 TaskTool "
                "（仅根会话的主 Agent 允许派发子任务）"
            )
            return (
                "Error: sub-agents are not allowed to dispatch further sub-tasks. "
                "Only the root agent can use the task tool."
            )

        if self._session_manager is None:
            return (
                "Error: session context not initialized. "
                "This tool must be used within an active session."
            )

        agent_names = _get_all_subagent_names()
        if sub_agent not in agent_names:
            return (
                f"Error: unknown sub-agent '{sub_agent}'. "
                f"Available: {', '.join(agent_names)}"
            )

        agent_config = next(a for a in _get_all_subagents() if a.name == sub_agent)

        logger.info(
            f"[TaskTool] 派发子任务: sub_agent={sub_agent}, "
            f"parent={self._parent_session_id}, goal={goal[:80]}..."
        )

        try:
            # Step 1: 创建子会话（前台等待模式，不后台运行）
            child_session = self._session_manager.create(
                title=goal[:50],
                agent_role=sub_agent,
                work_dir=self._work_dir,
                parent_id=self._parent_session_id,
            )

            logger.info(
                f"[TaskTool] 子会话已创建: {child_session.id}, "
                f"role={sub_agent}"
            )

            # Step 2: 执行子会话（前台等待）
            result = await child_session.prompt(goal=goal)

            # Step 3: 提取并返回结果
            if result.get("success"):
                content = result.get("content", "")
                turns = result.get("turns", 0)
                logger.info(
                    f"[TaskTool] 子任务完成: session={child_session.id}, "
                    f"turns={turns}, output_len={len(content)}"
                )
                return (
                    f"[Sub-agent '{sub_agent}' completed in {turns} turn(s)]\n\n"
                    f"{content}"
                )
            else:
                error = result.get("error", "unknown error")
                logger.error(
                    f"[TaskTool] 子任务失败: session={child_session.id}, "
                    f"error={error}"
                )
                return (
                    f"[Sub-agent '{sub_agent}' failed] Error: {error}"
                )

        except Exception as e:
            logger.error(f"[TaskTool] 子任务派发异常: {e}", exc_info=True)
            return f"Error: sub-agent dispatch failed - {str(e)}"
