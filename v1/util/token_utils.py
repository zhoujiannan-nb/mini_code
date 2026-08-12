"""Token 计算工具"""
import tiktoken
from loguru import logger
from typing import List, Dict, Any


def count_text_tokens(text: str, encoding_name: str = "cl100k_base") -> int:
    """
    计算文本的 token 数量
    
    Args:
        text: 要计算的文本
        encoding_name: 编码器名称，默认 cl100k_base (适用于 GPT-4/3.5)
        
    Returns:
        token 数量
    """
    try:
        encoding = tiktoken.get_encoding(encoding_name)
        return len(encoding.encode(text))
    except Exception as e:
        logger.warning(f"Token 计算失败：{e}，使用估算值")
        # 简单估算：平均每个字符约 0.3-0.5 个 token
        return int(len(text) * 0.4)


def count_messages_tokens(messages: List[Dict[str, Any]], encoding_name: str = "cl100k_base") -> int:
    """
    计算消息列表的 token 数量（适用于 OpenAI API 格式）
    
    Args:
        messages: 消息列表，格式为 [{"role": "user", "content": "..."}]
        encoding_name: 编码器名称
        
    Returns:
        估算的 token 数量
    """
    try:
        encoding = tiktoken.get_encoding(encoding_name)
        num_tokens = 0
        
        for message in messages:
            # 每条消息的基础开销
            num_tokens += 4  # role + content 标记
            
            # 计算内容的 token
            content = message.get("content", "")
            if isinstance(content, str):
                num_tokens += len(encoding.encode(content))
            
            # 如果有工具调用，也计算进去
            tool_calls = message.get("tool_calls")
            if tool_calls:
                for tool_call in tool_calls:
                    if "function" in tool_call:
                        func = tool_call["function"]
                        num_tokens += len(encoding.encode(func.get("name", "")))
                        num_tokens += len(encoding.encode(func.get("arguments", "{}")))
            
            # tool 角色的消息
            if "name" in message:
                num_tokens += len(encoding.encode(message["name"]))
        
        num_tokens += 2  # 结束标记
        return num_tokens
    
    except Exception as e:
        logger.warning(f"Token 计算失败：{e}，返回估算值")
        # 简单估算
        total_chars = sum(len(str(m.get("content", ""))) for m in messages)
        return int(total_chars * 0.4)


def check_token_limit(
    current_tokens: int,
    max_output_tokens: int,
    context_window: int,
    reserved_tokens: int = 2000
) -> Dict[str, Any]:
    """
    检查是否超过 token 限制
    
    Args:
        current_tokens: 当前已使用的 token 数（输入）
        max_output_tokens: 期望的最大输出 token 数
        context_window: 模型的总 context window
        reserved_tokens: 预留给系统提示词等的 token 数
        
    Returns:
        {
            "within_limit": bool,  # 是否在限制内
            "current_tokens": int,  # 当前 token 数
            "max_allowed_input": int,  # 最大允许的输入 token 数
            "exceeded_by": int  # 超出的 token 数（如果超出）
        }
    """
    max_allowed_input = context_window - max_output_tokens - reserved_tokens
    exceeded_by = max(0, current_tokens - max_allowed_input)
    
    return {
        "within_limit": current_tokens <= max_allowed_input,
        "current_tokens": current_tokens,
        "max_allowed_input": max_allowed_input,
        "exceeded_by": exceeded_by,
        "context_window": context_window,
        "max_output_tokens": max_output_tokens,
        "reserved_tokens": reserved_tokens
    }


def truncate_text_to_token_limit(
    text: str,
    max_tokens: int,
    encoding_name: str = "cl100k_base"
) -> str:
    """
    将文本截断到指定的 token 限制
    
    Args:
        text: 原始文本
        max_tokens: 最大 token 数
        encoding_name: 编码器名称
        
    Returns:
        截断后的文本
    """
    try:
        encoding = tiktoken.get_encoding(encoding_name)
        tokens = encoding.encode(text)
        
        if len(tokens) <= max_tokens:
            return text
        
        # 截断并解码
        truncated_tokens = tokens[:max_tokens]
        return encoding.decode(truncated_tokens)
    
    except Exception as e:
        logger.warning(f"Token 截断失败：{e}，使用字符截断")
        # 降级方案：按字符截断（保守估计）
        max_chars = int(max_tokens * 2.5)
        return text[:max_chars]
