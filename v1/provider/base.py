"""
Provider 抽象基类 —— 定义所有 LLM 后端必须实现的统一接口。
"""
import abc
import asyncio
import random
from typing import Optional, Dict

import aiohttp
from loguru import logger


class BaseProvider(abc.ABC):
    """LLM Provider 抽象基类"""

    def __init__(
        self,
        base_url: str,
        api_key: str,
        model_name: str,
        max_tokens: int = 8192,
        context_window: int = 32768,
        temperature: float = 0.7,
        top_p: float = 0.9,
        reserve_tokens: int = 2048,
    ):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.model_name = model_name
        self.max_tokens = max_tokens
        self.context_window = context_window
        self.temperature = temperature
        self.top_p = top_p
        self.reserve_tokens = reserve_tokens

        self._session: Optional[aiohttp.ClientSession] = None

        logger.info(
            f"[{self.provider_name}] 初始化：model={self.model_name} @ {self.base_url}"
        )

    # ── 子类必须标识自身名称 ──────────────────────────────
    @property
    @abc.abstractmethod
    def provider_name(self) -> str:
        """Provider 名称，用于日志和注册"""
        ...

    # ── 子类实现：构建请求细节 ─────────────────────────────
    @abc.abstractmethod
    def _build_url(self) -> str:
        """构建 chat completions 请求 URL"""
        ...

    @abc.abstractmethod
    def _build_headers(self) -> Dict[str, str]:
        """构建请求头"""
        ...

    def _build_payload(self, messages: list, **kwargs) -> Dict:
        """构建请求体（默认 OpenAI 兼容格式，子类可覆写）"""
        payload = {
            "model": self.model_name,
            "messages": messages,
            "temperature": kwargs.get("temperature", self.temperature),
            "max_tokens": kwargs.get("max_tokens", self.max_tokens),
            "stream": False,
        }
        if "tools" in kwargs:
            payload["tools"] = kwargs["tools"]
            payload["tool_choice"] = kwargs.get("tool_choice", "auto")
        return payload

    def _parse_response(self, result: Dict) -> Dict:
        """解析响应体（默认 OpenAI 兼容格式，子类可覆写）"""
        choices = result.get("choices", [])
        if not choices:
            return {"content": "", "error": "无返回结果"}

        message = choices[0].get("message", {})
        return {
            "content": message.get("content", ""),
            "tool_calls": message.get("tool_calls", []),
            "finish_reason": choices[0].get("finish_reason"),
            "usage": result.get("usage"),
        }

    # ── HTTP Session 管理 ─────────────────────────────────
    async def _get_session(self) -> aiohttp.ClientSession:
        """获取或创建 HTTP session"""
        if self._session is None or self._session.closed:
            self._session = aiohttp.ClientSession(headers=self._build_headers())
        return self._session

    async def close(self):
        """关闭 HTTP session"""
        if self._session and not self._session.closed:
            await self._session.close()
            logger.debug(f"[{self.provider_name}] session 已关闭")

    # ── 核心调用（带重试） ────────────────────────────────
    async def chat(self, messages: list, **kwargs) -> Dict:
        """
        统一聊天接口（内置重试机制）

        Args:
            messages: [{"role": "user", "content": "..."}, ...]
            **kwargs: temperature, max_tokens, tools, tool_choice 等

        Returns:
            {"content": str, "tool_calls": list, "finish_reason": str, "usage": dict}
        """
        max_retries = 3
        retry_count = 0

        while retry_count <= max_retries:
            try:
                url = self._build_url()
                payload = self._build_payload(messages, **kwargs)
                session = await self._get_session()

                async with session.post(url, json=payload) as response:
                    if response.status != 200:
                        error_text = await response.text()
                        logger.error(
                            f"[{self.provider_name}] 请求失败：{response.status}, {error_text}"
                        )

                        if self._is_token_error(error_text):
                            logger.error(f"[{self.provider_name}] Token 超限错误，不重试")
                            return {
                                "content": "",
                                "error": f"API 请求失败：{response.status} - {error_text}",
                            }

                        if retry_count < max_retries:
                            retry_count += 1
                            wait = self._retry_wait(retry_count)
                            logger.warning(
                                f"[{self.provider_name}] 将在 {wait}s 后重试 "
                                f"({retry_count}/{max_retries})"
                            )
                            await asyncio.sleep(wait)
                            continue
                        return {
                            "content": "",
                            "error": f"API 请求失败：{response.status} - {error_text}",
                        }

                    result = await response.json()
                    parsed = self._parse_response(result)

                    logger.debug(
                        f"[{self.provider_name}] 响应："
                        f"{len(parsed.get('content') or '')} 字符, "
                        f"tool_calls={len(parsed.get('tool_calls') or [])}, "
                        f"finish_reason={parsed.get('finish_reason')}"
                    )
                    return parsed

            except Exception as e:
                logger.exception(f"[{self.provider_name}] 调用异常：{e}")

                if self._is_token_error(str(e)):
                    logger.error(f"[{self.provider_name}] Token 超限错误，不重试")
                    raise

                if retry_count < max_retries:
                    retry_count += 1
                    wait = self._retry_wait(retry_count)
                    logger.warning(
                        f"[{self.provider_name}] 将在 {wait}s 后重试 "
                        f"({retry_count}/{max_retries})"
                    )
                    await asyncio.sleep(wait)
                    continue
                raise

    # ── 连接测试 ──────────────────────────────────────────
    async def test_connection(self) -> bool:
        """测试 Provider 连接是否可用"""
        try:
            resp = await self.chat([{"role": "user", "content": "Hi"}])
            ok = resp and "error" not in resp
            logger.info(f"[{self.provider_name}] 连接测试：{'成功' if ok else '失败'}")
            return ok
        except Exception as e:
            logger.error(f"[{self.provider_name}] 连接测试失败：{e}")
            return False

    # ── Token 容量查询 ────────────────────────────────────
    def get_context_window(self) -> int:
        return self.context_window

    def get_max_tokens(self) -> int:
        return self.max_tokens

    def get_max_input(self) -> int:
        return self.context_window - self.max_tokens - self.reserve_tokens

    # ── 内部工具方法 ──────────────────────────────────────
    @staticmethod
    def _is_token_error(text: str) -> bool:
        keywords = [
            "context length", "token limit", "max_tokens",
            "input tokens", "output tokens", "exceeds", "too long", "400",
        ]
        lower = text.lower()
        return any(kw in lower for kw in keywords)

    @staticmethod
    def _retry_wait(retry_count: int) -> float:
        return round(1.0 + random.uniform(0, 2.0), 2)
