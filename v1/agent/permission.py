"""工具权限配置模块。

定义 Agent 可用的工具及其操作权限，支持两级权限控制：
  - 工具级：  git: "allow" / "deny"
  - 子命令级：exec: {"rm": "deny", "ls": "allow"}
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Any


# ============================================================================
#  权限动作枚举
# ============================================================================

class PermissionAction(str, Enum):
    """权限动作标识"""
    ALLOW = "allow"
    DENY = "deny"


# ============================================================================
#  单个工具权限
# ============================================================================

@dataclass(frozen=True)
class ToolPermission:
    """单个工具的权限配置。

    两种模式（互斥）：
      1. 简单模式 — 直接设定 action（ALLOW / DENY）
      2. 子命令模式 — sub_commands 字典对子命令逐一设定权限

    Attributes:
        action:       工具整体的允许/拒绝（简单模式）
        sub_commands: 子命令 → 动作 映射（子命令模式）
    """
    action: PermissionAction = PermissionAction.ALLOW
    sub_commands: dict[str, PermissionAction] = field(default_factory=dict)

    # ---- 工厂方法 ----

    @staticmethod
    def allow() -> ToolPermission:
        """允许该工具的所有操作"""
        return ToolPermission(action=PermissionAction.ALLOW)

    @staticmethod
    def deny() -> ToolPermission:
        """拒绝该工具的所有操作"""
        return ToolPermission(action=PermissionAction.DENY)

    @staticmethod
    def commands(mapping: dict[str, str]) -> ToolPermission:
        """子命令级权限控制。

        Args:
            mapping: {"rm": "deny", "ls": "allow"} 形式的字典

        Returns:
            子命令模式的 ToolPermission（action 默认 DENY，仅显式列出的子命令生效）
        """
        sub = {k: PermissionAction(v) for k, v in mapping.items()}
        return ToolPermission(action=PermissionAction.DENY, sub_commands=sub)

    # ---- 查询方法 ----

    def is_allowed(self, sub_command: str | None = None) -> bool:
        """判断该工具（或子命令）是否被允许。

        Args:
            sub_command: 子命令名称，为 None 时仅检查工具整体权限

        Returns:
            True 表示允许执行
        """
        if sub_command is None:
            return self.action == PermissionAction.ALLOW

        # 子命令模式：先查子命令映射，未命中则回退到工具整体 action
        if self.sub_commands:
            cmd_action = self.sub_commands.get(sub_command)
            if cmd_action is not None:
                return cmd_action == PermissionAction.ALLOW
        return self.action == PermissionAction.ALLOW


# ============================================================================
#  权限配置（工具名 → ToolPermission 映射）
# ============================================================================

@dataclass(frozen=True)
class PermissionConfig:
    """Agent 的完整工具权限配置。

    以工具注册名为 key，ToolPermission 为 value。
    未列出的工具视为 **拒绝**。

    Example::

        PermissionConfig(
            git=ToolPermission.allow(),
            exec=ToolPermission.commands({"rm": "deny", "ls": "allow"}),
        )
    """
    tools: dict[str, ToolPermission] = field(default_factory=dict)

    # ---- 便捷构造 ----

    @staticmethod
    def from_dict(raw: dict[str, Any]) -> PermissionConfig:
        """从字典构建 PermissionConfig。

        支持两种 value 格式：
          - str:    "allow" / "deny"  → 简单模式
          - dict:   {"rm": "deny", ...} → 子命令模式

        Example::

            PermissionConfig.from_dict({
                "read_file": "allow",
                "write_file": "deny",
                "exec": {"rm": "deny", "ls": "allow"},
            })
        """
        tools: dict[str, ToolPermission] = {}
        for name, value in raw.items():
            if isinstance(value, str):
                action = PermissionAction(value)
                tools[name] = ToolPermission(action=action)
            elif isinstance(value, dict):
                tools[name] = ToolPermission.commands(value)
            elif isinstance(value, ToolPermission):
                tools[name] = value
            else:
                raise TypeError(
                    f"工具 '{name}' 的权限值类型无效: {type(value)}，"
                    f"期望 str / dict / ToolPermission"
                )
        return PermissionConfig(tools=tools)

    # ---- 查询方法 ----

    def is_tool_allowed(self, tool_name: str, sub_command: str | None = None) -> bool:
        """判断工具（或子命令）是否被允许。

        Args:
            tool_name:   工具注册名
            sub_command: 可选的子命令名

        Returns:
            True 表示允许；未注册的工具一律返回 False
        """
        perm = self.tools.get(tool_name)
        if perm is None:
            return False
        return perm.is_allowed(sub_command)

    def allowed_tools(self) -> list[str]:
        """返回所有被允许的工具名称列表"""
        return [name for name, perm in self.tools.items()
                if perm.action == PermissionAction.ALLOW or perm.sub_commands]
