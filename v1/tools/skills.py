"""Skills tool — 扫描并管理内部/外部技能。

扫描规则：
  - 内部技能：项目根目录下 `skills/` 文件夹中的 SKILL.md
  - 外部技能：`.apiguide/skills/` 文件夹中的 SKILL.md

每个 SKILL.md 支持 YAML frontmatter 元数据：
  ---
  name: 技能名称
  description: 技能描述
  metadata:
    internal: true   # 标识为内部专业技能
  ---
"""

import re
from pathlib import Path
from typing import Any

from loguru import logger

from .base import Tool


_FRONTMATTER_RE = re.compile(
    r"^---\s*\n(.*?)\n---\s*\n",
    re.DOTALL,
)


def _parse_frontmatter(content: str) -> tuple[dict[str, Any], str]:
    """解析 SKILL.md 的 YAML frontmatter 和正文。

    为避免引入 PyYAML 依赖，采用轻量级解析：
    仅处理两层嵌套的 key: value 映射。

    Returns:
        (metadata_dict, body_text)
    """
    match = _FRONTMATTER_RE.match(content)
    if not match:
        return {}, content

    fm_text = match.group(1)
    body = content[match.end():]
    meta: dict[str, Any] = {}
    current_key: str | None = None
    current_sub: dict[str, Any] | None = None

    for line in fm_text.splitlines():
        stripped = line.strip()
        if not stripped:
            continue

        # 子键（缩进 ≥ 2 空格）
        if line.startswith("  ") or line.startswith("\t"):
            if current_key and current_sub is not None:
                if ":" in stripped:
                    sub_k, sub_v = stripped.split(":", 1)
                    sub_v = sub_v.strip()
                    current_sub[sub_k.strip()] = _coerce(sub_v)
            continue

        # 顶层键
        if ":" in stripped:
            key, val = stripped.split(":", 1)
            val = val.strip()
            current_key = key.strip()
            if val:
                meta[current_key] = _coerce(val)
                current_sub = None
            else:
                # 可能是嵌套字典
                current_sub = {}
                meta[current_key] = current_sub

    return meta, body.strip()


def _coerce(val: str) -> Any:
    """将字符串值转为合适的 Python 类型。"""
    if val.lower() in ("true", "yes"):
        return True
    if val.lower() in ("false", "no"):
        return False
    # 去除引号
    if (val.startswith('"') and val.endswith('"')) or \
       (val.startswith("'") and val.endswith("'")):
        return val[1:-1]
    return val


def _parse_skill_file(path: Path) -> dict[str, Any] | None:
    """解析单个 SKILL.md 文件，返回技能元数据。"""
    try:
        content = path.read_text(encoding="utf-8")
        meta, body = _parse_frontmatter(content)

        name = meta.get("name", path.parent.name)
        description = meta.get("description", "")
        internal = False
        metadata = meta.get("metadata", {})
        if isinstance(metadata, dict):
            internal = metadata.get("internal", False)

        return {
            "name": name,
            "description": description,
            "path": str(path),
            "internal": internal,
            "body": body,
            "metadata": metadata if isinstance(metadata, dict) else {},
        }
    except Exception as e:
        logger.warning(f"[skills] 解析技能文件失败 {path}: {e}")
        return None


def scan_skills(
    internal_dir: Path | None = None,
    external_dir: Path | None = None,
) -> list[dict[str, Any]]:
    """扫描内部和外部技能目录。

    Args:
        internal_dir: 内部技能目录（skills/）
        external_dir: 外部技能目录（.apiguide/skills/）

    Returns:
        合并后的技能列表
    """
    skills: list[dict[str, Any]] = []

    for label, directory in [("internal", internal_dir), ("external", external_dir)]:
        if directory is None or not directory.is_dir():
            continue
        for skill_md in sorted(directory.rglob("SKILL.md")):
            info = _parse_skill_file(skill_md)
            if info:
                info["source"] = label
                skills.append(info)

    return skills


class SkillsTool(Tool):
    """扫描并返回可用技能列表，支持按来源过滤和读取技能内容。"""

    def __init__(
        self,
        workspace: Path | None = None,
        internal_skills_dir: Path | None = None,
        external_skills_dir: Path | None = None,
    ):
        """
        Args:
            workspace:          工作区根目录
            internal_skills_dir: 内部技能目录，默认 {workspace}/skills
            external_skills_dir: 外部技能目录，默认 {workspace}/.apiguide/skills
        """
        self._workspace = workspace or Path.cwd()
        self._internal_dir = internal_skills_dir or (self._workspace / "skills")
        self._external_dir = external_skills_dir or (self._workspace / ".apiguide" / "skills")

    @property
    def name(self) -> str:
        return "skills"

    @property
    def description(self) -> str:
        return (
            "List available skills (internal and external). "
            "Use action='list' to get all skills, action='read' to read a specific skill's content. "
            "Internal skills are in the skills/ directory, external skills are in .apiguide/skills/."
        )

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "action": {
                    "type": "string",
                    "description": "Action to perform: 'list' (default) to list all skills, 'read' to read a skill's full content",
                    "enum": ["list", "read"],
                },
                "source": {
                    "type": "string",
                    "description": "Filter by source: 'internal' (built-in), 'external' (project-specific). Omit for all.",
                    "enum": ["internal", "external"],
                },
                "skill_name": {
                    "type": "string",
                    "description": "Skill name to read (required when action='read')",
                },
            },
            "required": [],
        }

    async def execute(
        self,
        action: str = "list",
        source: str | None = None,
        skill_name: str | None = None,
        **kwargs: Any,
    ) -> str:
        try:
            if action == "read":
                return self._read_skill(skill_name)
            return self._list_skills(source)
        except Exception as e:
            logger.error(f"[skills] 执行失败: {e}")
            return f"Error: {e}"

    def _list_skills(self, source: str | None = None) -> str:
        """列出可用技能。"""
        all_skills = scan_skills(self._internal_dir, self._external_dir)

        if source:
            all_skills = [s for s in all_skills if s["source"] == source]

        if not all_skills:
            return "没有找到可用的技能。"

        lines = ["可用技能列表：\n"]
        for s in all_skills:
            tag = "🔧 内部" if s["source"] == "internal" else "🌐 外部"
            lines.append(f"  [{tag}] {s['name']}")
            if s["description"]:
                lines.append(f"    描述: {s['description']}")
            lines.append(f"    路径: {s['path']}")
            lines.append("")

        lines.append(f"共 {len(all_skills)} 个技能")
        return "\n".join(lines)

    def _read_skill(self, skill_name: str | None) -> str:
        """读取指定技能的完整内容。"""
        if not skill_name:
            return "Error: 请提供 skill_name 参数指定要读取的技能名称"

        all_skills = scan_skills(self._internal_dir, self._external_dir)
        matched = [s for s in all_skills if s["name"] == skill_name]

        if not matched:
            available = [s["name"] for s in all_skills]
            return (
                f"Error: 未找到名为 '{skill_name}' 的技能。\n"
                f"可用技能: {', '.join(available) if available else '无'}"
            )

        skill = matched[0]
        parts = [
            f"技能名称: {skill['name']}",
            f"来源: {'内部' if skill['source'] == 'internal' else '外部'}",
            f"路径: {skill['path']}",
        ]
        if skill["description"]:
            parts.append(f"描述: {skill['description']}")
        if skill["metadata"]:
            parts.append(f"元数据: {skill['metadata']}")
        parts.append(f"\n--- 技能内容 ---\n{skill['body']}")

        return "\n".join(parts)
