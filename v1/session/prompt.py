"""
Prompt 组装器 — 根据 Agent 配置和会话上下文构建系统提示词。

职责：
- 读取 Agent 角色的基础提示词
- 注入工具使用约束
- 注入会话上下文（工作目录、目标等）
"""

from pathlib import Path
from typing import Any, Optional

from loguru import logger

from agent.agent import AgentConfig, get_agent_config
from tools.registry import ToolRegistry
from tools.filesystem import ReadFileTool, WriteFileTool, EditFileTool, ListDirTool
from tools.shell import ExecTool
from tools.git import GitCloneTool, GitPullTool, GitDiffTool
from tools.task import TaskTool
from tools.skills import SkillsTool
from tools.api_manager import ApiManagerTool


# 提示词目录
_PROMPT_DIR = Path(__file__).parent.parent / "agent" / "prompts"


class PromptBuilder:
    """提示词组装器"""

    # 工具使用约束（所有 Agent 共享）
    TOOL_CONSTRAINTS = """
<tool_requirements>
# Tool Usage Instructions
- Tool calls must be made exclusively through structured tool_calls with valid JSON object parameters.
- By default, all independent tool calls within a single response are executed in a batch — parallel execution improves efficiency by 100x or more.
- It is prohibited to use text simulating the `<tool_call></tool_call>` format within the body of the text to make unauthorized calls.
- The format of the JSON parameters must be valid.
- Batch-call all tools that support parallel invocation to boost performance over 100x.
</tool_requirements>
"""


    def __init__(self, agent_config: Optional[AgentConfig] = None):
        """
        Args:
            agent_config: Agent 配置，为 None 时使用默认 build 配置
        """
        self.agent_config = agent_config or get_agent_config("build")
        self._task_tool: TaskTool | None = None  # 通过 enable_task_tool 按需创建
        self.tools = self._create_default_tools()

    def build_system_prompt(
        self,
        work_dir: str = "",
        extra_context: str = "",
    ) -> str:
        """
        构建完整的系统提示词。

        Args:
            work_dir: 工作目录路径
            extra_context: 额外上下文信息

        Returns:
            完整的系统提示词字符串
        """
        parts: list[str] = []

        # 1. Agent 角色基础提示词
        base_prompt = self._get_base_prompt()
        if base_prompt:
            parts.append(base_prompt)

        # 2. 工具使用约束
        parts.append(self.TOOL_CONSTRAINTS)

        # 3. 工作目录约束
        if work_dir:
            parts.append(f"""
# Working Directory

The current working directory is: {work_dir}
All file operations must be confined to this directory. Accessing paths outside this directory is prohibited.
""")

        # 5. 额外上下文
        if extra_context:
            parts.append(f"""
# Additional Context

{extra_context}
""")

        system_prompt = "\n".join(parts).strip()
        logger.debug(
            f"[PromptBuilder] 构建系统提示词完成，角色={self.agent_config.role.value}，"
            f"长度={len(system_prompt)} 字符"
        )
        return system_prompt

    def _get_base_prompt(self) -> str:
        """获取 Agent 角色的基础提示词"""
        # 优先使用配置中的 prompt
        if self.agent_config.prompt:
            return self.agent_config.prompt

        # 尝试从文件读取
        prompt_file = _PROMPT_DIR / f"{self.agent_config.role.value}.txt"
        if prompt_file.exists():
            return prompt_file.read_text(encoding="utf-8").strip()

        return ""

    def _create_default_tools(self) -> ToolRegistry:
        """创建默认工具集（不含 TaskTool，需通过 enable_task_tool 按需启用）"""
        registry = ToolRegistry()

        # 文件系统工具
        registry.register(ReadFileTool())
        registry.register(WriteFileTool())
        registry.register(EditFileTool())
        registry.register(ListDirTool())

        # Shell 工具
        registry.register(ExecTool(timeout=120))

        # Git 工具
        registry.register(GitCloneTool())
        registry.register(GitPullTool())
        registry.register(GitDiffTool())

        # Skills 工具
        registry.register(SkillsTool())

        # API 管理工具（生成/更新 + 删除）
        registry.register(ApiManagerTool())

        # 注意：TaskTool 不在此处注册，由 enable_task_tool() 按需启用
        # 只有根会话（无 parent_id 的主 Agent）才应拥有 TaskTool

        logger.debug(f"[PromptBuilder] 默认工具集已创建，共 {len(registry)} 个工具")
        return registry

    def enable_task_tool(
        self,
        session_manager: Any,
        session_id: str,
        work_dir: str,
    ):
        """启用 TaskTool（仅限根会话调用）。

        创建 TaskTool 实例、注册到工具集、并设置会话上下文。
        子 Agent 不应调用此方法，以防止无限嵌套派发。

        Args:
            session_manager: SessionManager 实例
            session_id: 当前会话 ID（作为父会话 ID）
            work_dir: 工作目录
        """
        self._task_tool = TaskTool()
        self._task_tool.set_session_context(
            session_manager=session_manager,
            parent_session_id=session_id,
            work_dir=work_dir,
        )
        self.tools.register(self._task_tool)
        logger.debug(f"[PromptBuilder] TaskTool 已启用，session={session_id}")

    def get_filtered_definitions(self, agent_config: Optional[AgentConfig] = None) -> list[dict]:
        """获取经过 agent 权限过滤的工具定义（OpenAI schema 格式）。

        Args:
            agent_config: Agent 配置，为 None 时使用当前实例的配置

        Returns:
            过滤后的工具定义列表，格式为 OpenAI function calling schema
        """
        if not self.tools:
            return []

        definitions = self.tools.get_definitions()
        if not definitions:
            return []

        config = agent_config or self.agent_config
        permissions = config.permissions

        # 如果没有配置权限，返回所有工具
        if not permissions.tools:
            return definitions

        # 按权限过滤
        return [
            tool_def for tool_def in definitions
            if permissions.is_tool_allowed(
                tool_def.get("function", {}).get("name", "")
            )
        ]

    def build_subagent_prompt(
        self,
        work_dir: str,
        parent_context: str = "",
    ) -> str:
        """
        构建子 Agent 的系统提示词。

        Args:
            work_dir: 工作目录
            parent_context: 父 Agent 提供的上下文

        Returns:
            子 Agent 系统提示词
        """
        extra = ""
        if parent_context:
            extra = f"""
# Parent Task Context

The following is background information provided by the parent task. Please complete your sub-task based on this context:

{parent_context}
"""
        return self.build_system_prompt(
            work_dir=work_dir,
            extra_context=extra,
        )
