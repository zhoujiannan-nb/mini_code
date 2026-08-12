"""
Session 管理器 — 会话生命周期管理与执行入口。

职责：
- 会话的创建、删除、查询、列表管理
- 会话的 prompt() 方法：组装提示词并启动 Agent Loop
- 会话状态和消息历史的持久化
"""

import asyncio
from pathlib import Path
from typing import Optional, List, Dict, Any, Callable

from loguru import logger

from agent.agent import AgentConfig, get_agent_config, AgentRole
from provider.model import ModelClient

from .store import SessionStore, SessionRecord
from .prompt import PromptBuilder
from .loop import AgentLoop


# ============================================================================
#  Session 类
# ============================================================================

class Session:
    """
    会话实例。

    封装一个完整的 Agent 会话，包含：
    - 会话元信息（ID、标题、状态等）
    - 消息历史
    - 提示词组装
    - Agent Loop 执行
    """

    def __init__(
        self,
        record: SessionRecord,
        store: SessionStore,
        model_client: ModelClient,
        agent_config: Optional[AgentConfig] = None,
        session_manager: Optional['SessionManager'] = None,
    ):
        self._record = record
        self._store = store
        self._model_client = model_client
        self._agent_config = agent_config or get_agent_config(record.agent_role)
        self._session_manager = session_manager

    # ── 属性访问 ──────────────────────────────────────────

    @property
    def id(self) -> str:
        """会话 ID"""
        return self._record.session_id

    @property
    def parent_id(self) -> Optional[str]:
        """父会话 ID"""
        return self._record.parent_id

    @property
    def title(self) -> str:
        """会话标题"""
        return self._record.title

    @property
    def status(self) -> str:
        """会话状态"""
        return self._record.status

    @property
    def work_dir(self) -> str:
        """工作目录"""
        return self._record.work_dir

    @property
    def agent_role(self) -> str:
        """Agent 角色"""
        return self._record.agent_role

    @property
    def messages(self) -> List[Dict[str, Any]]:
        """消息历史"""
        return self._record.messages.copy()

    @property
    def record(self) -> SessionRecord:
        """底层记录"""
        return self._record

    # ── 核心方法：prompt ──────────────────────────────────

    async def prompt(
        self,
        goal: str,
        agent_role: Optional[str] = None,
        extra_context: str = "",
        max_turns: Optional[int] = None,
        on_tool_call: Optional[Callable] = None,
    ) -> Dict[str, Any]:
        """
        执行会话：组装提示词并启动 Agent Loop。

        Args:
            goal: 本次执行的目标/任务描述
            agent_role: 指定 Agent 角色（可选，覆盖会话默认角色）
            extra_context: 额外上下文
            max_turns: 最大循环轮次（可选，覆盖配置默认值）
            on_tool_call: 工具调用回调

        Returns:
            {
                "success": bool,
                "content": str,
                "turns": int,
                "error": str | None
            }
        """
        # 更新状态为运行中
        self._update_status("running")

        try:
            # 1. 确定 Agent 配置
            config = self._agent_config
            if agent_role and agent_role != self._record.agent_role:
                config = get_agent_config(agent_role)
                self._record.agent_role = agent_role

            # 2. 组装系统提示词
            prompt_builder = PromptBuilder(config)

            # 3. 仅根会话启用 TaskTool（子 Agent 禁止派发，防止无限嵌套）
            is_root_session = not self._record.parent_id
            if config.mode == "primary" and self._session_manager is not None and is_root_session:
                prompt_builder.enable_task_tool(
                    session_manager=self._session_manager,
                    session_id=self.id,
                    work_dir=self._record.work_dir,
                )

            system_prompt = prompt_builder.build_system_prompt(
                work_dir=self._record.work_dir,
                extra_context=extra_context,
            )

            logger.info(f"[Session] {self.id} 系统提示词: {system_prompt}")
            logger.info(f"[Session] {self.id} 用户输入: {goal}")
            
            # 4.. 获取经过 agent 权限过滤的格式化工具定义
            tool_definitions = prompt_builder.get_filtered_definitions(config)

            # 5. 创建 Agent Loop
            loop = AgentLoop(
                model_client=self._model_client,
                max_turns=max_turns or config.max_turns,
                on_tool_call=on_tool_call,
                tools=prompt_builder.tools,
                tool_definitions=tool_definitions,
            )

            # 6. 执行循环
            result = await loop.run(
                system_prompt=system_prompt,
                user_message=goal,
                messages=self._record.messages if self._record.messages else None,
            )
            
            # 7. 更新消息历史
            if result.get("messages"):
                self._record.messages = result["messages"]
                self._store.update(self._record)

            # 8. 更新状态
            if result.get("success"):
                self._update_status("completed")
            else:
                self._update_status("failed")

            logger.info(
                f"[Session] {self.id} 执行完成，"
                f"轮次={result.get('turns', 0)}，"
                f"成功={result.get('success', False)}"
            )

            return result

        except Exception as e:
            logger.error(f"[Session] {self.id} 执行异常: {e}", exc_info=True)
            self._update_status("failed")
            return {
                "success": False,
                "content": "",
                "turns": 0,
                "error": str(e),
            }

    # ── 内部方法 ──────────────────────────────────────────

    def _update_status(self, status: str):
        """更新会话状态"""
        self._record.status = status
        self._store.update(self._record)


# ============================================================================
#  SessionManager — 会话管理器
# ============================================================================

class SessionManager:
    """
    会话管理器。

    提供会话的创建、删除、查询、列表等管理功能，
    以及 ModelClient 的统一管理。
    """

    def __init__(
        self,
        model_client: ModelClient,
        db_path: Optional[str | Path] = None,
    ):
        """
        Args:
            model_client: LLM 客户端
            db_path: 数据库文件路径
        """
        self._model_client = model_client
        self._store = SessionStore(db_path)
        self._sessions: Dict[str, Session] = {}  # 缓存

        logger.info("[SessionManager] 初始化完成")

    # ── 会话 CRUD ─────────────────────────────────────────

    def create(
        self,
        title: str = "",
        agent_role: str = "build",
        work_dir: str = "",
        parent_id: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> Session:
        """
        创建新会话。

        Args:
            title: 会话标题
            agent_role: Agent 角色
            work_dir: 工作目录
            parent_id: 父会话 ID（subagent 场景）
            metadata: 扩展元信息

        Returns:
            新创建的 Session 实例
        """
        record = SessionRecord(
            title=title,
            agent_role=agent_role,
            work_dir=work_dir,
            parent_id=parent_id,
            metadata=metadata or {},
        )

        # 保存到数据库
        self._store.create(record)

        # 创建 Session 实例
        session = Session(
            record=record,
            store=self._store,
            model_client=self._model_client,
            session_manager=self,
        )

        # 缓存
        self._sessions[record.session_id] = session

        logger.info(
            f"[SessionManager] 创建会话: {record.session_id}，"
            f"角色={agent_role}，标题={title}，"
            f"parent_id={parent_id or '无'}"
        )
        return session

    def get(self, session_id: str) -> Optional[Session]:
        """
        获取会话。

        Args:
            session_id: 会话 ID

        Returns:
            Session 实例，不存在返回 None
        """
        # 先查缓存
        if session_id in self._sessions:
            return self._sessions[session_id]

        # 从数据库加载
        record = self._store.get(session_id)
        if record is None:
            return None

        session = Session(
            record=record,
            store=self._store,
            model_client=self._model_client,
            session_manager=self,
        )
        self._sessions[session_id] = session
        return session

    def delete(self, session_id: str) -> bool:
        """
        删除会话。

        Args:
            session_id: 会话 ID

        Returns:
            是否删除成功
        """
        # 清除缓存
        self._sessions.pop(session_id, None)

        # 从数据库删除
        deleted = self._store.delete(session_id)
        if deleted:
            logger.info(f"[SessionManager] 删除会话: {session_id}")
        return deleted

    def list(
        self,
        parent_id: Optional[str] = None,
        status: Optional[str] = None,
        limit: int = 50,
        offset: int = 0,
    ) -> List[Session]:
        """
        列出会话。

        Args:
            parent_id: 父会话 ID 过滤
            status: 状态过滤
            limit: 返回数量限制
            offset: 偏移量

        Returns:
            Session 列表
        """
        records = self._store.list_sessions(
            parent_id=parent_id,
            status=status,
            limit=limit,
            offset=offset,
        )

        sessions = []
        for record in records:
            session = Session(
                record=record,
                store=self._store,
                model_client=self._model_client,
                session_manager=self,
            )
            self._sessions[record.session_id] = session
            sessions.append(session)

        return sessions

    def update_title(self, session_id: str, title: str) -> bool:
        """
        更新会话标题。

        Args:
            session_id: 会话 ID
            title: 新标题

        Returns:
            是否更新成功
        """
        session = self.get(session_id)
        if session is None:
            return False

        session._record.title = title
        return self._store.update(session._record)

    def clear_messages(self, session_id: str) -> bool:
        """
        清空会话消息历史。

        Args:
            session_id: 会话 ID

        Returns:
            是否清空成功
        """
        session = self.get(session_id)
        if session is None:
            return False

        session._record.messages = []
        return self._store.update(session._record)

    # ── 快捷方法 ──────────────────────────────────────────

    async def run_task(
        self,
        goal: str,
        title: str = "",
        agent_role: str = "build",
        work_dir: str = "",
        parent_id: Optional[str] = None,
        max_turns: Optional[int] = None,
    ) -> Dict[str, Any]:
        """
        快捷方法：创建会话并执行任务。

        Args:
            goal: 任务目标
            title: 会话标题（可选）
            agent_role: Agent 角色
            work_dir: 工作目录
            parent_id: 父会话 ID
            max_turns: 最大轮次

        Returns:
            执行结果
        """
        session = self.create(
            title=title or goal[:50],
            agent_role=agent_role,
            work_dir=work_dir,
            parent_id=parent_id,
        )

        return await session.prompt(
            goal=goal,
            max_turns=max_turns,
        )
