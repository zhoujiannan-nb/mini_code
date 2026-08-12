"""
Ollama Provider —— 对接 Ollama 本地推理（OpenAI 兼容 API）。

默认端点: http://localhost:11434/v1
"""
from typing import Dict

from provider.base import BaseProvider


class OllamaProvider(BaseProvider):
    """Ollama 本地推理 Provider"""

    @property
    def provider_name(self) -> str:
        return "ollama"

    def _build_url(self) -> str:
        return f"{self.base_url}/chat/completions"

    def _build_headers(self) -> Dict[str, str]:
        headers = {"Content-Type": "application/json"}
        # Ollama 通常不需要 API Key，但如果配置了则带上
        if self.api_key and self.api_key != "not-needed":
            headers["Authorization"] = f"Bearer {self.api_key}"
        return headers
