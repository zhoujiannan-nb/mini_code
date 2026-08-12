"""Agent Package —— 多角色 Agent 架构"""

from .agent import (
    AgentConfig,
    AgentRole,
    get_agent_config,
    list_all_agents,
)
from .permission import (
    PermissionAction,
    ToolPermission,
    PermissionConfig,
)

__all__ = [
    # 核心类
    "AgentConfig",
    "AgentRole",
    # 权限
    "PermissionAction",
    "ToolPermission",
    "PermissionConfig",
    # 工厂函数
    "get_agent_config",
    "list_all_agents",
]
