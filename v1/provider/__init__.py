"""
provider —— LLM 后端统一抽象层。

公共 API:
    ModelClient      统一客户端门面
    BaseProvider      Provider 抽象基类
    VLLMProvider      vLLM 实现
    OllamaProvider    Ollama 实现
"""
from provider.base import BaseProvider
from provider.vllm import VLLMProvider
from provider.ollama import OllamaProvider
from provider.model import ModelClient, PROVIDER_REGISTRY

__all__ = [
    "ModelClient",
    "BaseProvider",
    "VLLMProvider",
    "OllamaProvider",
    "PROVIDER_REGISTRY",
]
