"""
Session 持久化存储 — SQLite 实现。

将会话数据（包括消息历史、元信息）持久化到本地 SQLite 数据库。
"""

import json
import uuid
from dataclasses import dataclass, field, asdict
from datetime import datetime
from pathlib import Path
from typing import Optional, List, Dict, Any

from loguru import logger
from util.db import Database


# ============================================================================
#  数据结构
# ============================================================================

@dataclass
class SessionRecord:
    """会话记录"""
    session_id: str = field(default_factory=lambda: uuid.uuid4().hex[:12])
    parent_id: Optional[str] = None          # 父会话 ID（subagent 场景）
    title: str = ""                          # 会话标题
    agent_role: str = "build"                # Agent 角色
    work_dir: str = ""                       # 工作目录
    status: str = "created"                  # created | running | completed | failed
    messages: List[Dict[str, Any]] = field(default_factory=list)  # 消息历史
    metadata: Dict[str, Any] = field(default_factory=dict)        # 扩展元信息
    created_at: str = field(default_factory=lambda: datetime.now().isoformat())
    updated_at: str = field(default_factory=lambda: datetime.now().isoformat())


# ============================================================================
#  存储层
# ============================================================================

class SessionStore:
    """SQLite 会话存储"""

    def __init__(self, db_path: str | Path = None):
        """
        Args:
            db_path: 数据库文件路径，默认为项目根目录下的 agent.db
        """
        if db_path is None:
            db_path = Path(__file__).parent.parent / "agent.db"
        self.db = Database(db_path)
        self._init_db()
        logger.info(f"[SessionStore] 数据库初始化完成: {self.db.db_path}")

    def _init_db(self):
        """初始化数据库表"""
        self.db.init_table("""
            CREATE TABLE IF NOT EXISTS sessions (
                session_id   TEXT PRIMARY KEY,
                parent_id    TEXT,
                title        TEXT DEFAULT '',
                agent_role   TEXT DEFAULT 'build',
                work_dir     TEXT DEFAULT '',
                status       TEXT DEFAULT 'created',
                messages     TEXT DEFAULT '[]',
                metadata     TEXT DEFAULT '{}',
                created_at   TEXT,
                updated_at   TEXT
            )
        """)
        self.db.init_index("""
            CREATE INDEX IF NOT EXISTS idx_sessions_parent 
            ON sessions(parent_id)
        """)
        self.db.init_index("""
            CREATE INDEX IF NOT EXISTS idx_sessions_status 
            ON sessions(status)
        """)

    # ── CRUD 操作 ──────────────────────────────────────────

    def create(self, record: SessionRecord) -> SessionRecord:
        """创建会话"""
        now = datetime.now().isoformat()
        record.created_at = now
        record.updated_at = now

        self.db.execute(
            """INSERT INTO sessions 
               (session_id, parent_id, title, agent_role, work_dir, 
                status, messages, metadata, created_at, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                record.session_id,
                record.parent_id,
                record.title,
                record.agent_role,
                record.work_dir,
                record.status,
                json.dumps(record.messages, ensure_ascii=False),
                json.dumps(record.metadata, ensure_ascii=False),
                record.created_at,
                record.updated_at,
            ),
        )

        logger.info(f"[SessionStore] 创建会话: {record.session_id}")
        return record

    def get(self, session_id: str) -> Optional[SessionRecord]:
        """获取会话"""
        row = self.db.fetchone(
            "SELECT * FROM sessions WHERE session_id = ?", (session_id,)
        )

        if row is None:
            return None

        return SessionRecord(
            session_id=row["session_id"],
            parent_id=row["parent_id"],
            title=row["title"],
            agent_role=row["agent_role"],
            work_dir=row["work_dir"],
            status=row["status"],
            messages=json.loads(row["messages"]),
            metadata=json.loads(row["metadata"]),
            created_at=row["created_at"],
            updated_at=row["updated_at"],
        )

    def update(self, record: SessionRecord) -> bool:
        """更新会话"""
        record.updated_at = datetime.now().isoformat()

        cursor = self.db.execute(
            """UPDATE sessions SET
               parent_id = ?, title = ?, agent_role = ?, work_dir = ?,
               status = ?, messages = ?, metadata = ?, updated_at = ?
               WHERE session_id = ?""",
            (
                record.parent_id,
                record.title,
                record.agent_role,
                record.work_dir,
                record.status,
                json.dumps(record.messages, ensure_ascii=False),
                json.dumps(record.metadata, ensure_ascii=False),
                record.updated_at,
                record.session_id,
            ),
        )
        return cursor.rowcount > 0

    def delete(self, session_id: str) -> bool:
        """删除会话（同时删除子会话）"""
        # 先删除子会话
        self.db.execute(
            "DELETE FROM sessions WHERE parent_id = ?", (session_id,)
        )
        # 再删除自身
        cursor = self.db.execute(
            "DELETE FROM sessions WHERE session_id = ?", (session_id,)
        )
        deleted = cursor.rowcount > 0

        if deleted:
            logger.info(f"[SessionStore] 删除会话: {session_id}")
        return deleted

    def list_sessions(
        self,
        parent_id: Optional[str] = None,
        status: Optional[str] = None,
        limit: int = 50,
        offset: int = 0,
    ) -> List[SessionRecord]:
        """列出会话"""
        query = "SELECT * FROM sessions WHERE 1=1"
        params: list = []

        if parent_id is not None:
            query += " AND parent_id = ?"
            params.append(parent_id)
        if status is not None:
            query += " AND status = ?"
            params.append(status)

        query += " ORDER BY updated_at DESC LIMIT ? OFFSET ?"
        params.extend([limit, offset])

        rows = self.db.fetchall(query, tuple(params))

        return [
            SessionRecord(
                session_id=row["session_id"],
                parent_id=row["parent_id"],
                title=row["title"],
                agent_role=row["agent_role"],
                work_dir=row["work_dir"],
                status=row["status"],
                messages=json.loads(row["messages"]),
                metadata=json.loads(row["metadata"]),
                created_at=row["created_at"],
                updated_at=row["updated_at"],
            )
            for row in rows
        ]

    def append_message(self, session_id: str, message: Dict[str, Any]) -> bool:
        """追加单条消息到会话历史"""
        record = self.get(session_id)
        if record is None:
            return False

        record.messages.append(message)
        return self.update(record)

    def clear_messages(self, session_id: str) -> bool:
        """清空会话消息历史"""
        record = self.get(session_id)
        if record is None:
            return False

        record.messages = []
        return self.update(record)
