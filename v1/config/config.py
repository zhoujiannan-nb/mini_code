"""项目配置管理"""

@dataclass(frozen=True, slots=True)
class ModelConfig:
    """模型配置"""
    provider: str = "vllm"
    base_url: str = "http://localhost:8000/v1"
    api_key: str = "not-needed"
    model_name: str = ""
    max_tokens: int = 8192
    context_window: int = 65536
    temperature: float = 0.7
    top_p: float = 0.9
    reserve_tokens: int = 2048


