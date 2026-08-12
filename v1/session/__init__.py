"""
Session 模块 — 会话管理与 Agent Loop 执行引擎。

本模块提供：
- Session: 会话生命周期管理（创建/删除/查询/执行）
- SessionStore: SQLite 持久化存储
- PromptBuilder: 根据 Agent 配置组装系统提示词
- AgentLoop: 核心循环引擎，驱动 LLM 调用与工具执行
- ContextCompactor: 上下文压缩，自动管理 Token 预算
"""

from .session import Session, SessionManager
from .store import SessionStore
from .prompt import PromptBuilder
from .loop import AgentLoop
from .compaction import ContextCompactor

__all__ = [
    "Session",
    "SessionManager",
    "SessionStore",
    "PromptBuilder",
    "AgentLoop",
    "ContextCompactor",
]
