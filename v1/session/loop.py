"""
Agent 循环引擎 — 驱动 LLM 调用与工具执行的核心循环。

职责：
- 接收系统提示词和用户消息，启动 Agent Loop
- 支持多轮对话，直到任务完成或达到最大轮次
- 工具调用支持并发执行
- 自动管理消息历史和 Token 预算
"""

import asyncio
import json
import re
import uuid
import httpx
from typing import Optional, List, Dict, Any, Callable

from loguru import logger

# 日志推送到 web.py
WEB_BASE_URL = "http://localhost:7500"

async def push_agent_log(event_type, data):
    """推送日志事件到 web.py"""
    payload = {'type': event_type, **data}
    try:
        async with httpx.AsyncClient(timeout=2) as client:
            await client.post(f"{WEB_BASE_URL}/api/task/log", json=payload)
    except Exception as e:
        logger.debug(f"推送日志失败: {e}")


from provider.model import ModelClient
from tools.registry import ToolRegistry
from tools.filesystem import ReadFileTool, WriteFileTool, EditFileTool, ListDirTool
from tools.shell import ExecTool
from tools.git import GitCloneTool, GitPullTool, GitDiffTool
from tools.skills import SkillsTool
from .compaction import ContextCompactor


# Token 工具延迟导入
def _count_messages_tokens(messages: list) -> int:
    """估算消息列表的 token 数量"""
    try:
        from util.token_utils import count_messages_tokens
        return count_messages_tokens(messages)
    except ImportError:
        total = 0
        for msg in messages:
            content = msg.get("content", "")
            if isinstance(content, str):
                total += len(content) // 4
        return total


class AgentLoop:
    """
    Agent 循环引擎。

    给定系统提示词和用户消息，驱动 LLM 进行多轮对话和工具调用，
    直到任务完成（模型输出最终答案）或达到最大轮次。
    """

    def __init__(
        self,
        model_client: ModelClient,
        max_turns: int = 999,
        on_tool_call: Optional[Callable] = None,
        tools: Optional[ToolRegistry] = None,
        tool_definitions: Optional[List[Dict[str, Any]]] = None,
        compact_threshold: float = 0.85,
    ):
        """
        Args:
            model_client: LLM 客户端
            max_turns: 最大循环轮次
            on_tool_call: 工具调用回调 (tool_name: str, params: dict, result: str) -> None
            tools: 工具注册表（用于工具执行，可选，为 None 时使用默认工具集）
            tool_definitions: 格式化的工具定义列表（OpenAI schema，用于 LLM API 调用）
            compact_threshold: 上下文压缩触发阈值（0~1，默认 0.85）
        """
        self.model_client = model_client
        self.max_turns = max_turns
        self.turn_reserve = 4096  
        self.on_tool_call = on_tool_call
        self.tools = tools if tools is not None else self._create_default_tools()
        self.tool_definitions = tool_definitions if tool_definitions is not None else self.tools.get_definitions()
        self._compactor = ContextCompactor(
            model_client=model_client,
            threshold=compact_threshold,
        )

        logger.info(
            f"[AgentLoop] 初始化完成，max_turns={max_turns}，"
            f"工具数={len(self.tools)}，压缩阈值={compact_threshold:.0%}"
        )

    def _create_default_tools(self) -> ToolRegistry:
        """创建默认工具集"""
        registry = ToolRegistry()

        registry.register(ReadFileTool())
        registry.register(WriteFileTool())
        registry.register(EditFileTool())
        registry.register(ListDirTool())
        registry.register(ExecTool(timeout=120))
        registry.register(GitCloneTool())
        registry.register(GitPullTool())
        registry.register(GitDiffTool())
        registry.register(SkillsTool())

        logger.info(f"[AgentLoop] 默认工具集已创建，共 {len(registry)} 个工具")
        return registry


    def _init_messages(
        self,
        system_prompt: str,
        user_message: str,
        messages: Optional[List[Dict[str, Any]]] = None,
    ) -> List[Dict[str, Any]]:
        """构建初始消息列表。"""
        if messages:
            full_messages = messages.copy()
            if not full_messages or full_messages[0].get("role") != "system":
                full_messages.insert(
                    0, {"role": "system", "content": system_prompt}
                )
            full_messages.append({"role": "user", "content": user_message})
        else:
            full_messages = [
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_message},
            ]
        return full_messages


    async def _compact_if_needed(
        self,
        full_messages: List[Dict[str, Any]],
    ) -> List[Dict[str, Any]]:
        """按需压缩上下文，返回压缩后的消息列表。"""
        max_input = self.model_client.get_max_input()
        return await self._compactor.check_and_compress(
            full_messages, _count_messages_tokens, max_input,
        )


    async def _call_llm(
        self,
        full_messages: List[Dict[str, Any]],
    ) -> Dict[str, Any]:
        """
        调用 LLM 获取响应。

        Returns:
            成功 → LLM 响应字典；失败 → {"error": "..."} 字典。
        """
        # Token 预算检查
        max_input = self.model_client.get_max_input()
        current = _count_messages_tokens(full_messages)
        if current > max_input - self.turn_reserve:
            return {"error": f"上下文 Token 超限 ({current}/{max_input})"}

        response = await self.model_client.chat(
            full_messages, tools=self.tool_definitions
        )
        if not response or "error" in response:
            error = (
                response.get("error", "LLM 返回空响应")
                if response
                else "LLM 返回空响应"
            )
            return {"error": error}
        return response


    @staticmethod
    def _is_error(llm_response: Dict[str, Any]) -> bool:
        """判断 LLM 响应是否为错误。"""
        return "error" in llm_response


    @staticmethod
    def _append_tool_call_error(
        full_messages: List[Dict[str, Any]],
        error_content: str,
    ) -> None:
        """处理工具调用格式错误，追加纠正提示到消息历史。"""
        full_messages.append({
            "role": "assistant",
            "content": error_content,
        })
        full_messages.append({
            "role": "user",
            "content": (
                "上述工具调用格式错误。你输出了文本格式的工具调用标签，且无法解析"
                "结构化 tool_calls 才是正确的调用方式。\n\n"
                "要求：\n"
                "1. 工具调用必须通过结构化 tool_calls 发起\n"
                "2. JSON 参数必须是合法格式\n\n"
                "请重新执行。。"
            ),
        })

    async def _handle_tool_calls(
        self,
        full_messages: List[Dict[str, Any]],
        text_content: str,
        tool_calls: List[Dict[str, Any]],
    ) -> None:
        """执行工具调用并将结果写入消息历史。"""
        # 记录 assistant 的工具调用意图
        full_messages.append({
            "role": "assistant",
            "tool_calls": tool_calls,
            "content": text_content or None,
        })

        # 并发执行
        results = await self._execute_tools_concurrent(tool_calls)

        # 将结果逐条写入消息历史并触发回调
        for tc, result in zip(tool_calls, results):
            tc_id = tc.get("id", "")
            tc_name = (
                tc.get("function", {}).get("name", "")
                if "function" in tc
                else tc.get("name", "")
            )
            full_messages.append({
                "role": "tool",
                "tool_call_id": tc_id,
                "name": tc_name,
                "content": result,
            })
            if self.on_tool_call:
                self.on_tool_call(tc_name, {}, result)

    async def _process_response(
        self,
        parsed: Dict[str, Any],
        full_messages: List[Dict[str, Any]],
    ) -> bool:
        """
        处理解析后的 LLM 响应。

        Returns:
            True → 继续循环；False → 停止循环。
        """
        text_content = parsed["content"]
        tool_calls = parsed["tool_calls"]

        if text_content:
            logger.info(f"[AgentLoop] LLM 响应: {text_content}")
        if tool_calls:
            logger.info(f"[AgentLoop] LLM 工具调用: {tool_calls}")

        await self._log_llm_response(text_content, tool_calls)

        # 内容过滤 → 停止
        if parsed["finish_reason"] == "content_filter":
            return False

        # 工具调用格式错误 → 纠正并继续
        if parsed.get("tool_call_error"):
            self._append_tool_call_error(
                full_messages,
                parsed.get("tool_call_error_content", ""),
            )
            return True

        # 有工具调用 → 执行并继续
        if tool_calls:
            await self._handle_tool_calls(
                full_messages, text_content, tool_calls
            )
            return True

        # 纯文本响应 → 停止
        return False


    async def _error_result(
        self,
        llm_response: Dict[str, Any],
        full_messages: List[Dict[str, Any]],
        turn_count: int,
    ) -> Dict[str, Any]:
        """构造错误结果并推送任务失败事件。"""
        error_msg = llm_response.get("error", "未知错误")
        await push_agent_log(
            "task_done",
            {"status": "failed", "message": error_msg},
        )
        return {
            "success": False,
            "content": "",
            "messages": full_messages,
            "turns": turn_count,
            "error": error_msg,
        }

    @staticmethod
    def _success_result(
        content: str,
        full_messages: List[Dict[str, Any]],
        turn_count: int,
    ) -> Dict[str, Any]:
        """构造成功结果。"""
        return {
            "success": True,
            "content": content,
            "messages": full_messages,
            "turns": turn_count,
            "error": None,
        }

    @staticmethod
    async def _max_turns_result(
        full_messages: List[Dict[str, Any]],
        max_turns: int,
    ) -> Dict[str, Any]:
        """构造达到最大轮次的结果。"""
        error_msg = f"达到最大循环轮次 ({max_turns})"
        await push_agent_log(
            "task_done",
            {"status": "failed", "message": error_msg},
        )
        return {
            "success": False,
            "content": "",
            "messages": full_messages,
            "turns": max_turns,
            "error": error_msg,
        }


    async def run(
        self,
        system_prompt: str,
        user_message: str,
        messages: Optional[List[Dict[str, Any]]] = None,
    ) -> Dict[str, Any]:
        """
        运行 Agent 循环。

        Args:
            system_prompt: 系统提示词
            user_message: 用户消息
            messages: 初始消息历史（可选，用于续接会话）

        Returns:
            {
                "success": bool,
                "content": str,
                "messages": list,
                "turns": int,
                "error": str | None
            }
        """        
        full_messages = self._init_messages(system_prompt, user_message, messages)
        logger.info(
            f"[AgentLoop] 开始执行，"
            f"初始 token={_count_messages_tokens(full_messages)}"
            f"/{self.model_client.get_max_input()}"
        )

        for turn_count in range(1, self.max_turns + 1):
            logger.info(f"[AgentLoop] === 第 {turn_count} 轮 ===")

            # 压缩上下文
            full_messages = await self._compact_if_needed(full_messages)

            # LLM
            llm_response = await self._call_llm(full_messages)
            if self._is_error(llm_response):
                return await self._error_result(
                    llm_response, full_messages, turn_count
                )

            # 处理响应
            parsed = self._parse_response(llm_response)
            should_continue = await self._process_response(parsed, full_messages)

            # 检查是否完成
            if not should_continue:
                return self._success_result(
                    parsed["content"], full_messages, turn_count
                )

        # 达到最大轮次
        return await self._max_turns_result(full_messages, self.max_turns)

    async def _execute_tools_concurrent(
        self, tool_calls: List[Dict[str, Any]]
    ) -> List[str]:
        """
        并发执行多个工具调用。

        Args:
            tool_calls: 工具调用列表

        Returns:
            工具执行结果列表（与 tool_calls 一一对应）
        """
        tasks = []
        for tc in tool_calls:
            tasks.append(self._execute_single_tool(tc))

        results = await asyncio.gather(*tasks, return_exceptions=True)

        # 处理异常
        final_results = []
        for i, result in enumerate(results):
            if isinstance(result, Exception):
                logger.error(f"[AgentLoop] 工具执行异常: {result}")
                final_results.append(f"Error: 工具执行失败 - {str(result)}")
            else:
                final_results.append(result)

        return final_results

    async def _execute_single_tool(self, tc_data: Dict[str, Any]) -> str:
        """
        执行单个工具调用。

        Args:
            tc_data: 工具调用数据

        Returns:
            工具执行结果字符串
        """
        # 兼容两种格式
        if "function" in tc_data:
            func_data = tc_data.get("function", {})
            tool_name = func_data.get("name", "")
            arguments_str = func_data.get("arguments", "{}")
        else:
            tool_name = tc_data.get("name", "")
            arguments_str = tc_data.get("arguments", "{}")

        # 解析 arguments
        if isinstance(arguments_str, str):
            try:
                arguments = json.loads(arguments_str)
            except json.JSONDecodeError:
                logger.error(f"[AgentLoop] 工具参数 JSON 解析失败: {arguments_str}")
                arguments = {}
        else:
            arguments = arguments_str

        # 执行工具
        logger.info(f"[AgentLoop] 执行工具: {tool_name}, 参数: {arguments}")
        try:
            result = await self.tools.execute(tool_name, arguments)
            result_str = result if isinstance(result, str) else str(result)
            logger.info(
                f"[AgentLoop] 工具 {tool_name} 执行完成，结果长度={len(result_str)}"
            )
            return result_str
        except Exception as e:
            logger.error(f"[AgentLoop] 工具 {tool_name} 执行失败: {e}")
            return f"Error: 工具执行失败 - {str(e)}"

    def _parse_response(self, llm_response: Dict[str, Any]) -> Dict[str, Any]:
        """
        解析 LLM 响应。

        Args:
            llm_response: LLM 原始响应

        Returns:
            解析结果字典
        """
        content = llm_response.get("content", "")
        tool_calls = llm_response.get("tool_calls", [])
        finish_reason = llm_response.get("finish_reason", "")

        should_terminate = False
        tool_call_error = False
        tool_call_error_content = ""

        # 检测文本格式的工具调用
        if content and not tool_calls:
            extracted = self._try_extract_text_tool_call(content)
            if extracted:
                if extracted["status"] == "success":
                    tool_calls = extracted["tool_calls"]
                    content = ""
                    logger.info(
                        f"[AgentLoop] 从文本中恢复 {len(tool_calls)} 个工具调用"
                    )
                else:
                    tool_call_error = True
                    tool_call_error_content = "工具调用解析失败,使用正确的格式和正确的参数调用工具"
                    content = ""
                    logger.warning("[AgentLoop] 文本格式工具调用解析失败")

        # 判断终止条件
        if finish_reason == "stop" and not tool_calls:
            should_terminate = True

        return {
            "content": content,
            "tool_calls": tool_calls,
            "finish_reason": finish_reason,
            "should_terminate": should_terminate,
            "tool_call_error": tool_call_error,
            "tool_call_error_content": tool_call_error_content,
        }

    def _try_extract_text_tool_call(self, text: str) -> Optional[Dict[str, Any]]:
        """
        尝试从文本中提取工具调用（兼容模型输出文本格式的情况）。

        Args:
            text: 模型输出的文本

        Returns:
            提取结果，None 表示未检测到工具调用
        """
        if not text or not text.strip():
            return None

        text = text.strip()

        # 匹配 tool_call 标签
        tag_matches = list(
            re.finditer(r"<tool_call>\s*(.*?)\s*</tool_call>", text, re.DOTALL)
        )
        if not tag_matches:
            tag_matches = list(re.finditer(r"<tool_call>(.*)", text, re.DOTALL))

        if not tag_matches:
            return None

        tool_calls = []
        for match in tag_matches:
            content = match.group(1).strip()

            # 尝试解析 JSON
            parsed = self._parse_tool_call_json(content)
            if parsed is not None:
                tool_calls.append(parsed)
                continue

            # 尝试补全 JSON
            parsed = self._parse_tool_call_json(content + "}")
            if parsed is not None:
                tool_calls.append(parsed)
                continue

            # 解析失败
            return {"status": "error", "content": match.group(0)}

        if tool_calls:
            return {"status": "success", "tool_calls": tool_calls}
        return None

    def _parse_tool_call_json(self, content: str) -> Optional[Dict[str, Any]]:
        """
        解析工具调用 JSON。

        Args:
            content: JSON 字符串

        Returns:
            标准化的工具调用字典，None 表示解析失败
        """
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError:
            return None

        if not isinstance(parsed, dict):
            return None

        # 标准化格式
        if "function" in parsed:
            func_data = parsed["function"]
            if not isinstance(func_data, dict):
                return None
            tool_name = func_data.get("name", "")
            arguments = func_data.get("arguments", "{}")
        elif "name" in parsed:
            tool_name = parsed.get("name", "")
            arguments = parsed.get("arguments", "{}")
        else:
            return None

        if not tool_name:
            return None

        if not isinstance(arguments, str):
            arguments = json.dumps(arguments, ensure_ascii=False)

        return {
            "id": f"call_{uuid.uuid4().hex[:8]}",
            "type": "function",
            "function": {
                "name": tool_name,
                "arguments": arguments,
            },
        }
