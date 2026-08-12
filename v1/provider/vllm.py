"""
vLLM Provider —— 对接 vLLM 推理引擎（OpenAI 兼容 API）。

默认端点: http://localhost:8000/v1
"""
from typing import Dict

from provider.base import BaseProvider


class VLLMProvider(BaseProvider):
    """vLLM 推理引擎 Provider"""

    @property
    def provider_name(self) -> str:
        return "vllm"

    def _build_url(self) -> str:
        return f"{self.base_url}/chat/completions"

    def _build_headers(self) -> Dict[str, str]:
        return {
            "Authorization": f"Bearer {self.api_key}",
            "Content-Type": "application/json",
        }
