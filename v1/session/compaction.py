"""
Context Compactor — 上下文压缩模块。

当会话消息历史的 token 用量达到可用输入 token 的 85% 时，
自动触发压缩，将长对话历史压缩为结构化摘要，释放上下文空间。
"""

from pathlib import Path
from typing import List, Dict, Any, Optional

from loguru import logger

from provider.model import ModelClient


# 压缩提示词目录
_PROMPT_DIR = Path(__file__).parent.parent / "agent" / "prompts"    # 压缩提示词目录

# 压缩触发阈值：可用输入 token 的 85%
COMPACT_THRESHOLD = 0.85    # 压缩触发阈值：可用输入 token 的 85%

# 压缩时保留最近 N 条消息不参与压缩（保持最新上下文连贯）
RECENT_KEEP = 6

# 压缩摘要模板
_COMPACT_USER_TEMPLATE = """Please compress the following conversation history into a concise structured summary.

**Goal**: {goal}

**Conversation History**:
{history}

**Requirements**:
- Preserve key facts, file paths, important data from tool results, and decision rationale
- Use exact names and numbers, do not generalize
- Focus on currently unfinished tasks and latest progress
- Output strictly following the 5 sections below

**Output Format** (use exactly these section headers):

## Goal
(Overall task goal and original user intent)

## Current Instructions
(The most recent key user instructions or subtasks. Keep the latest one fully intact)

## Discoveries
(Key observations, important tool results, problem diagnoses, data, etc.)

## Accomplished
(Completed work, modified files, verified parts)

## Relevant Files
(List of currently active or important files with brief status)

---
The compressed context will be used to continue the task. Ensure the next agent can seamlessly pick up from here."""


class ContextCompactor:
    """上下文压缩器。

    在 Agent Loop 中按需调用，检查 token 用量并在超过阈值时压缩对话历史。
    """

    def __init__(
        self,
        model_client: ModelClient,
        threshold: float = COMPACT_THRESHOLD,
        recent_keep: int = RECENT_KEEP,
    ):
        """
        Args:
            model_client: LLM 客户端（用于执行压缩）
            threshold: 触发压缩的 token 占比阈值（0~1）
            recent_keep: 压缩时保留最近 N 条消息不参与压缩
        """
        self.model_client = model_client
        self.threshold = threshold
        self.recent_keep = recent_keep
        self._system_prompt = self._load_compaction_prompt()

    def _load_compaction_prompt(self) -> str:
        """加载压缩系统提示词"""
        prompt_file = _PROMPT_DIR / "compaction.txt"
        if prompt_file.exists():
            return prompt_file.read_text(encoding="utf-8").strip()
        return (
            "You are a professional context compression assistant. "
            "Compress conversation history into a concise structured summary."
        )

    def should_compact(self, current_tokens: int, max_input: int) -> bool:
        """检查是否需要压缩。

        Args:
            current_tokens: 当前消息的 token 数
            max_input: 模型可用最大输入 token 数

        Returns:
            是否超过阈值需要压缩
        """
        if max_input <= 0:
            return False
        ratio = current_tokens / max_input
        return ratio >= self.threshold

    async def compact(
        self,
        full_messages: List[Dict[str, Any]],
    ) -> List[Dict[str, Any]]:
        """执行压缩，返回压缩后的新消息列表。

        压缩策略：
        1. 保留 system 消息（原样）
        2. 提取 goal（第一个 user 消息）
        3. 将中间历史（去掉最近 N 条）发送给 LLM 压缩
        4. 将压缩摘要作为新 user 消息，追加最近 N 条消息

        Args:
            full_messages: 完整消息历史

        Returns:
            压缩后的新消息列表
        """
        # 分离 system 消息
        system_msg = None
        non_system = []
        for msg in full_messages:
            if msg.get("role") == "system" and system_msg is None:
                system_msg = msg
            else:
                non_system.append(msg)

        if not non_system:
            logger.warning("[Compactor] 无非系统消息可压缩")
            return full_messages

        # 提取 goal（第一个 user 消息）
        goal = ""
        for msg in non_system:
            if msg.get("role") == "user":
                goal = msg.get("content", "")
                if isinstance(goal, list):
                    # 多模态消息，提取文本部分
                    goal = " ".join(
                        part.get("text", "") for part in goal if isinstance(part, dict)
                    )
                break

        # 分离最近保留的消息和待压缩的历史
        if len(non_system) <= self.recent_keep:
            # 消息太少，不需要压缩
            logger.info("[Compactor] 消息数量不足，跳过压缩")
            return full_messages

        to_compact = non_system[: -self.recent_keep]
        recent = non_system[-self.recent_keep :]

        # 构造历史文本
        history_text = self._format_messages_for_compaction(to_compact)

        # 调用 LLM 压缩
        logger.info(
            f"[Compactor] 开始压缩，待压缩消息={len(to_compact)}条，"
            f"保留最近={len(recent)}条"
        )

        compressed_summary = await self._call_llm_compact(goal, history_text)

        if not compressed_summary:
            logger.warning("[Compactor] 压缩失败，保留原始消息")
            return full_messages

        # 重组消息列表
        new_messages = []

        if system_msg:
            new_messages.append(system_msg)

        # 压缩摘要作为 user 消息
        new_messages.append({
            "role": "user",
            "content": (
                "<previous-summary>\n"
                "The following is a compressed summary of earlier conversation history:\n\n"
                f"{compressed_summary}\n"
                "</previous-summary>"
            ),
        })

        # 摘要确认（assistant 回复，让模型知道收到了摘要）
        new_messages.append({
            "role": "assistant",
            "content": "Understood. I have the compressed context and will continue from where we left off.",
        })

        # 追加最近保留的消息
        new_messages.extend(recent)

        logger.info(
            f"[Compactor] 压缩完成，消息数: {len(full_messages)} -> {len(new_messages)}"
        )
        return new_messages

    async def check_and_compress(
        self,
        full_messages: List[Dict[str, Any]],
        count_tokens_fn,
        max_input: int,
    ) -> List[Dict[str, Any]]:
        """检查 token 用量并在需要时压缩。

        这是供 AgentLoop 调用的主入口——一次完成检查 + 压缩。

        Args:
            full_messages: 当前完整消息历史
            count_tokens_fn: 计算 token 数的函数 (messages) -> int
            max_input: 模型可用最大输入 token 数

        Returns:
            压缩后（或原样）的消息列表
        """
        current_tokens = count_tokens_fn(full_messages)

        if not self.should_compact(current_tokens, max_input):
            return full_messages

        logger.info(
            f"[Compactor] 触发压缩: {current_tokens}/{max_input} "
            f"({current_tokens / max_input:.1%})"
        )

        new_messages = await self.compact(full_messages)

        # 记录压缩效果
        new_tokens = count_tokens_fn(new_messages)
        logger.info(
            f"[Compactor] 压缩效果: {current_tokens} -> {new_tokens} tokens "
            f"(节省 {current_tokens - new_tokens})"
        )

        return new_messages

    def _format_messages_for_compaction(
        self, messages: List[Dict[str, Any]]
    ) -> str:
        """将消息列表格式化为可读文本，用于压缩提示。

        Args:
            messages: 待格式化的消息列表

        Returns:
            格式化后的文本
        """
        parts = []
        for msg in messages:
            role = msg.get("role", "unknown")
            content = msg.get("content", "")

            if content is None:
                content = ""

            # 多模态内容
            if isinstance(content, list):
                text_parts = []
                for part in content:
                    if isinstance(part, dict):
                        if part.get("type") == "text":
                            text_parts.append(part.get("text", ""))
                        elif part.get("type") == "tool_result":
                            text_parts.append(f"[Tool Result: {part.get('content', '')}]")
                content = "\n".join(text_parts)

            # 工具调用消息
            tool_calls = msg.get("tool_calls")
            if tool_calls:
                tc_desc = []
                for tc in tool_calls:
                    func = tc.get("function", {})
                    name = func.get("name", tc.get("name", ""))
                    args = func.get("arguments", tc.get("arguments", ""))
                    tc_desc.append(f"  - Call: {name}({args})")
                content += "\n".join(tc_desc)

            # 工具结果消息
            tool_call_id = msg.get("tool_call_id")
            if tool_call_id:
                name = msg.get("name", "")
                content = f"[Tool Result for {name}]: {content}"

            # 截断过长的内容
            if len(content) > 2000:
                content = content[:2000] + f"\n... [truncated, total {len(content)} chars]"

            parts.append(f"[{role.upper()}]: {content}")

        return "\n\n".join(parts)

    async def _call_llm_compact(self, goal: str, history_text: str) -> Optional[str]:
        """调用 LLM 执行压缩。

        Args:
            goal: 会话目标（第一个 user 消息）
            history_text: 格式化后的历史文本

        Returns:
            压缩后的摘要文本，失败返回 None
        """
        user_prompt = _COMPACT_USER_TEMPLATE.format(
            goal=goal,
            history=history_text,
        )

        messages = [
            {"role": "system", "content": self._system_prompt},
            {"role": "user", "content": user_prompt},
        ]

        try:
            response = await self.model_client.chat(
                messages,
                temperature=0.3,  # 压缩用低温度，保证一致性
                max_tokens=4096,
            )

            if response and "error" not in response:
                content = response.get("content", "")
                if content:
                    return content.strip()

            logger.warning(f"[Compactor] LLM 压缩返回异常: {response}")
            return None

        except Exception as e:
            logger.error(f"[Compactor] LLM 压缩调用失败: {e}")
            return None
