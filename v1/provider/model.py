"""
ModelClient —— LLM Provider 统一门面。

根据配置中的 provider 字段自动创建对应的 Provider 实例，
对外暴露统一的 chat / test_connection / close 等接口。

新增 Provider 只需：
1. 在 provider/ 下新建 xxx.py，继承 BaseProvider
2. 在本文件 PROVIDER_REGISTRY 中注册
"""
from typing import Dict, Type

from loguru import logger

from provider.base import BaseProvider
from provider.vllm import VLLMProvider
from provider.ollama import OllamaProvider

# ── Provider 注册表 ────────────────────────────────────────
# key: 配置文件中 provider 字段的值（不区分大小写）
PROVIDER_REGISTRY: Dict[str, Type[BaseProvider]] = {
    "vllm": VLLMProvider,
    "ollama": OllamaProvider,
}


class ModelClient:
    """
    LLM 统一客户端（门面模式）。

    用法:
        # 从 dict 创建
        client = ModelClient({"provider": "vllm", "base_url": "...", ...})

        # 从 ModelConfig 创建
        client = ModelClient(config.model)

        # 调用
        result = await client.chat(messages)
        await client.close()
    """

    def __init__(self, config: dict | object):
        provider_type, params = self._extract_config(config)

        cls = PROVIDER_REGISTRY.get(provider_type.lower())
        if cls is None:
            available = ", ".join(PROVIDER_REGISTRY.keys())
            raise ValueError(
                f"未知的 provider: '{provider_type}'，"
                f"可用: {available}"
            )

        self._provider: BaseProvider = cls(**params)
        self.provider_type = provider_type

    # ── 配置解析 ──────────────────────────────────────────
    @staticmethod
    def _extract_config(config) -> tuple[str, dict]:
        """从 dict 或 dataclass 配置中提取 provider 类型和参数"""
        if isinstance(config, dict):
            provider_type = config.get("provider", "vllm")
            params = {
                "base_url": config.get("base_url", "http://localhost:8000/v1"),
                "api_key": config.get("api_key", "not-needed"),
                "model_name": config.get("model_name", "qwen-7b"),
                "max_tokens": config.get("max_tokens", 8192),
                "context_window": config.get("context_window", 32768),
                "temperature": config.get("temperature", 0.7),
                "top_p": config.get("top_p", 0.9),
                "reserve_tokens": config.get("reserve_tokens", 2048),
            }
        else:
            # ModelConfig dataclass
            provider_type = getattr(config, "provider", "vllm")
            params = {
                "base_url": config.base_url,
                "api_key": config.api_key,
                "model_name": config.model_name,
                "max_tokens": config.max_tokens,
                "context_window": config.context_window,
                "temperature": config.temperature,
                "top_p": config.top_p,
                "reserve_tokens": getattr(config, "reserve_tokens", 2048),
            }
        return provider_type, params

    # ── 代理到 Provider ───────────────────────────────────
    async def chat(self, messages: list, **kwargs) -> Dict:
        """聊天接口（委托给底层 Provider）"""
        return await self._provider.chat(messages, **kwargs)

    async def close(self):
        """关闭底层 Provider 的 HTTP session"""
        await self._provider.close()

    async def test_connection(self) -> bool:
        """测试连接是否可用"""
        return await self._provider.test_connection()

    def get_context_window(self) -> int:
        return self._provider.get_context_window()

    def get_max_tokens(self) -> int:
        return self._provider.get_max_tokens()

    def get_max_input(self) -> int:
        return self._provider.get_max_input()

    # ── 调试 / _repr_ ────────────────────────────────────
    def __repr__(self) -> str:
        return (
            f"ModelClient(provider={self.provider_type}, "
            f"model={self._provider.model_name})"
        )
