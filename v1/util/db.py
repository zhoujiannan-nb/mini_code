"""
通用 SQLite 数据库管理器。

提供统一的数据库连接管理和基础 CRUD 操作，
供项目各模块复用。
"""

import json
import sqlite3
from pathlib import Path
from typing import Optional, List, Dict, Any

from loguru import logger


class Database:
    """
    SQLite 数据库管理器。

    提供连接管理、表初始化、通用 CRUD 操作。
    """

    def __init__(self, db_path: str | Path):
        """
        Args:
            db_path: 数据库文件路径
        """
        self.db_path = Path(db_path)
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        logger.info(f"[Database] 初始化: {self.db_path}")

    def _get_conn(self) -> sqlite3.Connection:
        """获取数据库连接"""
        conn = sqlite3.connect(str(self.db_path))
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA journal_mode=WAL")
        return conn

    def execute(self, sql: str, params: tuple = ()) -> sqlite3.Cursor:
        """
        执行 SQL 语句。

        Args:
            sql: SQL 语句
            params: 参数

        Returns:
            游标对象
        """
        with self._get_conn() as conn:
            cursor = conn.execute(sql, params)
            conn.commit()
            return cursor

    def executemany(self, sql: str, params_list: list[tuple]) -> int:
        """
        批量执行 SQL 语句。

        Args:
            sql: SQL 语句
            params_list: 参数列表

        Returns:
            影响的行数
        """
        with self._get_conn() as conn:
            cursor = conn.executemany(sql, params_list)
            conn.commit()
            return cursor.rowcount

    def fetchone(self, sql: str, params: tuple = ()) -> Optional[Dict[str, Any]]:
        """
        查询单条记录。

        Args:
            sql: SQL 语句
            params: 参数

        Returns:
            记录字典，无结果返回 None
        """
        with self._get_conn() as conn:
            row = conn.execute(sql, params).fetchone()
            return dict(row) if row else None

    def fetchall(self, sql: str, params: tuple = ()) -> List[Dict[str, Any]]:
        """
        查询多条记录。

        Args:
            sql: SQL 语句
            params: 参数

        Returns:
            记录字典列表
        """
        with self._get_conn() as conn:
            rows = conn.execute(sql, params).fetchall()
            return [dict(row) for row in rows]

    def init_table(self, create_sql: str):
        """
        初始化表（如果不存在）。

        Args:
            create_sql: CREATE TABLE IF NOT EXISTS 语句
        """
        with self._get_conn() as conn:
            conn.execute(create_sql)
            conn.commit()

    def init_index(self, index_sql: str):
        """
        初始化索引（如果不存在）。

        Args:
            index_sql: CREATE INDEX IF NOT EXISTS 语句
        """
        with self._get_conn() as conn:
            conn.execute(index_sql)
            conn.commit()

    def table_exists(self, table_name: str) -> bool:
        """
        检查表是否存在。

        Args:
            table_name: 表名

        Returns:
            是否存在
        """
        result = self.fetchone(
            "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
            (table_name,),
        )
        return result is not None

    def delete_db(self):
        """删除数据库文件（谨慎使用）"""
        if self.db_path.exists():
            self.db_path.unlink()
            logger.warning(f"[Database] 已删除数据库: {self.db_path}")


# ============================================================================
#  全局数据库实例（可选）
# ============================================================================

# 默认数据库路径（项目根目录）
_DEFAULT_DB_DIR = Path(__file__).parent.parent


def get_default_db(name: str = "app.db") -> Database:
    """
    获取默认数据库实例。

    Args:
        name: 数据库文件名

    Returns:
        Database 实例
    """
    db_path = _DEFAULT_DB_DIR / name
    return Database(db_path)
