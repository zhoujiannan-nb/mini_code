"""Agent模块入口 — 多角色Agent架构。

本模块定义各Agent角色的配置信息，供上层模块据此构建实例并驱动运行。

内置角色：
  - build:      主Agent，拥有全部工具权限，可通过 task 工具派发子任务
  - explore:    只读探索Agent，专注代码搜索与文件浏览
  - generate:   API文档生成Agent，专职生成接口文档（子Agent，只读+生成工具）
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path
from typing import Any

from loguru import logger

from .permission import PermissionConfig


# 提示词目录
_PROMPT_DIR = Path(__file__).parent / "prompts"

# 自定义 Agent 扫描目录（项目根目录下的 .apiguide/agents/）
_CUSTOM_AGENTS_DIRS: list[Path] = [
    Path.cwd() / ".apiguide" / "agents",
]

# 自定义 Agent 注册表（懒加载）
_custom_agents: dict[str, AgentConfig] | None = None


class AgentRole(str, Enum):
    """Agent角色标识"""
    BUILD = "build"
    PLAN = "plan"
    EXPLORE = "explore"
    GENERATE = "generate"


@dataclass
class AgentConfig:
    """Agent配置。

    包含角色定义、系统提示词、工具权限、运行约束等全部信息，
    上层模块据此创建对应的Agent实例。
    """
    name: str
    role: AgentRole
    description: str = ""
    prompt: str = ""
    mode: str = "primary"           # primary | subagent
    max_turns: int = 99
    hidden: bool = False
    # 工具权限配置（未列出的工具视为拒绝）
    permissions: PermissionConfig = field(default_factory=PermissionConfig)


# 所有 Agent 共享的基础工具权限（各角色在此基础上覆盖）
_BASE_PERMISSIONS: dict[str, Any] = {
    "read_file":  "allow",
    "list_dir":   "allow",
    "exec":       "allow",
    "skills":     "allow",
}


def _merge_permissions(*overrides: dict[str, Any]) -> PermissionConfig:
    """以基础权限为底，逐层合并覆盖项，返回 PermissionConfig。"""
    merged = {**_BASE_PERMISSIONS}
    for ov in overrides:
        merged.update(ov)
    return PermissionConfig.from_dict(merged)


def build_agent_config() -> AgentConfig:
    """主Agent（build）配置。

    拥有全部工具权限，可派发子任务给 explore / general 子Agent。
    """
    prompt_file = _PROMPT_DIR / "build.txt"
    prompt_text = prompt_file.read_text(encoding="utf-8").strip() if prompt_file.exists() else ""
    return AgentConfig(
        name="build",
        role=AgentRole.BUILD,
        description="The default agent. Executes tools based on configured permissions.",
        prompt=prompt_text,
        mode="primary",
        max_turns=99,
        permissions=_merge_permissions({
            "write_file": "allow",
            "edit_file":  "allow",
            "git_clone":  "allow",
            "git_pull":   "allow",
            "git_diff":   "allow",
            "task":       "allow",
        }),
    )


def explore_agent_config() -> AgentConfig:
    """探索Agent（explore）配置。

    只读访问权限，专注代码搜索与文件浏览，不可修改任何文件。
    """
    prompt_file = _PROMPT_DIR / "explore.txt"
    prompt_text = prompt_file.read_text(encoding="utf-8").strip() if prompt_file.exists() else ""

    return AgentConfig(
        name="explore",
        role=AgentRole.EXPLORE,
        description=(
            "Fast agent specialized for exploring codebases. "
            "Use this when you need to quickly find files by patterns, "
            "search code for keywords, or answer questions about the codebase."
        ),
        prompt=prompt_text,
        mode="subagent",
        max_turns=99,
        # 只读：移除写入类工具，exec 禁止危险子命令
        permissions=_merge_permissions({
            "write_file": "deny",
            "edit_file":  "deny",
            "exec":       {"rm": "deny", "del": "deny"},
            "skills":     "allow",  # 允许查看技能列表
        }),
    )

def generate_agent_config() -> AgentConfig:
    """API 文档生成 Agent（generate）配置。

    子 Agent，专职负责 API 接口文档生成。
    拥有读权限 + API 生成工具，禁止写入代码文件。
    由主 Agent 通过 task 工具派发。
    """
    prompt_file = _PROMPT_DIR / "generate.txt"
    prompt_text = prompt_file.read_text(encoding="utf-8").strip() if prompt_file.exists() else ""

    return AgentConfig(
        name="generate",
        role=AgentRole.GENERATE,
        description=(
            "API documentation generator sub-agent. "
            "Analyzes source code and generates structured API docs using the api_manager tool. "
            "Has read-only filesystem access plus the API generation tool."
        ),
        prompt=prompt_text,
        mode="subagent",
        max_turns=99,
        # 只读 + API 生成工具，禁止写入代码文件
        permissions=_merge_permissions({
            "write_file":      "deny",
            "edit_file":       "deny",
            "api_manager": "allow",
            "exec":            {"rm": "deny", "del": "deny"},
            "skills":          "allow",
            "git_clone":       "deny",
            "git_pull":        "deny",
            "git_diff":        "allow",
        }),
    )


_AGENT_CONFIG_FACTORIES: dict[str, callable] = {
    AgentRole.BUILD: build_agent_config,
    AgentRole.EXPLORE: explore_agent_config,
    AgentRole.GENERATE: generate_agent_config,
}


def _load_custom_agents() -> dict[str, AgentConfig]:
    """扫描 .apiguide/agents/ 目录，加载自定义 Agent 配置。

    规则：
      - 目录下每个 .md 文件代表一个自定义 Agent
      - 文件名（去 .md 后缀）即为 agent name（也是 role 标识）
      - 文件内容作为系统提示词（prompt）
      - 默认角色为 subagent，权限为只读 + skills

    Returns:
        {agent_name: AgentConfig} 映射
    """
    agents: dict[str, AgentConfig] = {}
    for agents_dir in _CUSTOM_AGENTS_DIRS:
        if not agents_dir.is_dir():
            continue
        for md_file in agents_dir.glob("*.md"):
            name = md_file.stem
            prompt_text = md_file.read_text(encoding="utf-8").strip()
            agents[name] = AgentConfig(
                name=name,
                role=AgentRole.BUILD,  # placeholder, custom agents use name-based lookup
                description=f"Custom agent: {name}",
                prompt=prompt_text,
                mode="subagent",
                max_turns=99,
                permissions=_merge_permissions({
                    "write_file":      "deny",
                    "edit_file":       "deny",
                    "exec":            {"rm": "deny", "del": "deny"},
                    "skills":          "allow",
                    "api_manager": "deny",
                    "git_clone":       "deny",
                    "git_pull":        "deny",
                    "git_diff":        "deny",
                }),
            )
            logger.info(f"[Agent] 加载自定义 Agent: {name} (from {md_file})")
    return agents


def _get_custom_agents() -> dict[str, AgentConfig]:
    """获取自定义 Agent 注册表（懒加载，首次访问时扫描目录）。"""
    global _custom_agents
    if _custom_agents is None:
        _custom_agents = _load_custom_agents()
    return _custom_agents


def get_agent_config(role: str | AgentRole) -> AgentConfig:
    """根据角色获取Agent配置。

    查找顺序：
      1. 自定义 Agent（.apiguide/agents/ 目录下的 .md 文件）
      2. 内置 Agent（_AGENT_CONFIG_FACTORIES 注册表）

    Args:
        role: Agent角色（字符串或枚举值）

    Returns:
        对应的AgentConfig实例

    Raises:
        ValueError: 未知角色
    """
    name = role.value if isinstance(role, AgentRole) else role

    # 优先查找自定义 Agent
    custom = _get_custom_agents()
    if name in custom:
        return custom[name]

    # 回退到内置 Agent
    if isinstance(role, str):
        try:
            role = AgentRole(role)
        except ValueError:
            available = list(_AGENT_CONFIG_FACTORIES.keys()) + list(custom.keys())
            raise ValueError(
                f"未知Agent角色: {role}, 可用角色: {available}"
            )

    factory = _AGENT_CONFIG_FACTORIES.get(role)
    if factory is None:
        available = list(_AGENT_CONFIG_FACTORIES.keys()) + list(custom.keys())
        raise ValueError(
            f"未知Agent角色: {role}, 可用角色: {available}"
        )
    return factory()


def list_all_agents() -> dict[str, str]:
    """列出所有可用 Agent（内置 + 自定义），返回 {name: description} 映射。"""
    result: dict[str, str] = {}
    for role, factory in _AGENT_CONFIG_FACTORIES.items():
        config = factory()
        result[config.name] = config.description
    for name, config in _get_custom_agents().items():
        result[name] = config.description
    return result
