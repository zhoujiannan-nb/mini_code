"""AI模型输出JSON解析工具

应对模型输出JSON的各种不规范情况：
- ```json ... ``` 标准代码块
- ```JSON_START ... ```JSON_END 自定义标记
- ```JSON_START ... ``` 半自定义标记
- ```JSON ... ``` 大写变体
- 直接输出JSON文本（无代码块包裹）
- JSON前后有多余文本/注释
- 代码块未正确闭合
"""
import json
import re
from typing import Any, Optional, Union
from loguru import logger


def extract_json(result: str, *, default: Any = None) -> Any:
    """
    从AI模型返回的文本中提取并解析JSON

    按优先级依次尝试多种提取策略，尽可能容错：
    1. ```JSON_START ... ```JSON_END 自定义标记
    2. ```JSON_START ... ``` 半自定义标记
    3. ```json ... ``` 标准markdown代码块（大小写兼容）
    4. 直接解析整段文本
    5. 尝试找到文本中的第一个JSON结构（{...} 或 [...]）

    Args:
        result: AI模型返回的原始文本
        default: 解析失败时返回的默认值，默认为 None

    Returns:
        解析后的Python对象（dict/list），失败时返回 default

    Examples:
        >>> extract_json('```json\\n{"key": "value"}\\n```')
        {'key': 'value'}
        >>> extract_json('some text {"key": "value"} more text')
        {'key': 'value'}
        >>> extract_json('invalid', default=[])
        []
    """
    if not result or not result.strip():
        logger.warning("extract_json: 输入为空")
        return default

    result = result.strip()

    # 策略1: ```JSON_START ... ```JSON_END
    json_match = re.search(r'```JSON_START\s*(.*?)\s*```JSON_END', result, re.DOTALL)
    if json_match:
        json_str = json_match.group(1).strip()
        parsed = _try_parse(json_str)
        if parsed is not None:
            return parsed

    # 策略2: ```JSON_START ... ``` (AI可能只用```关闭)
    json_start_match = re.search(r'```JSON_START\s*(.*)', result, re.DOTALL)
    if json_start_match:
        json_str = json_start_match.group(1).strip()
        json_str = re.sub(r'\s*```\s*$', '', json_str)
        parsed = _try_parse(json_str)
        if parsed is not None:
            return parsed

    # 策略3: ```json ... ``` 或 ```JSON ... ```（大小写兼容）
    json_code_match = re.search(r'```[jJ][sS][oO][nN]\s*(.*?)\s*```', result, re.DOTALL)
    if json_code_match:
        json_str = json_code_match.group(1).strip()
        parsed = _try_parse(json_str)
        if parsed is not None:
            return parsed

    # 策略3.5: ```json ... 但代码块未闭合
    json_code_unclosed = re.search(r'```[jJ][sS][oO][nN]\s*(.*)', result, re.DOTALL)
    if json_code_unclosed:
        json_str = json_code_unclosed.group(1).strip()
        json_str = re.sub(r'\s*```\s*$', '', json_str)
        parsed = _try_parse(json_str)
        if parsed is not None:
            return parsed

    # 策略4: 直接解析整段文本
    parsed = _try_parse(result)
    if parsed is not None:
        return parsed

    # 策略5: 暴力搜索第一个完整的JSON结构
    parsed = _find_json_in_text(result)
    if parsed is not None:
        return parsed

    logger.warning(f"extract_json: 所有策略均失败，原始内容前500字符: {result[:500]}")
    return default


def _try_parse(json_str: str) -> Optional[Any]:
    """尝试解析JSON字符串，失败返回None"""
    try:
        return json.loads(json_str)
    except (json.JSONDecodeError, ValueError):
        return None


def _find_json_in_text(text: str) -> Optional[Any]:
    """
    从文本中暴力搜索第一个完整的JSON结构

    通过括号匹配找到第一个完整的 {...} 或 [...] 块
    """
    # 先尝试找对象 {...}
    obj_start = text.find('{')
    if obj_start != -1:
        result = _extract_balanced(text, obj_start, '{', '}')
        if result is not None:
            return result

    # 再尝试找数组 [...]
    arr_start = text.find('[')
    if arr_start != -1:
        result = _extract_balanced(text, arr_start, '[', ']')
        if result is not None:
            return result

    return None


def _extract_balanced(text: str, start: int, open_char: str, close_char: str) -> Optional[Any]:
    """
    从指定位置提取平衡的括号内容并尝试解析

    处理字符串内的嵌套括号（不会误判字符串中的括号）
    """
    depth = 0
    in_string = False
    escape_next = False
    i = start

    while i < len(text):
        ch = text[i]

        if escape_next:
            escape_next = False
            i += 1
            continue

        if ch == '\\' and in_string:
            escape_next = True
            i += 1
            continue

        if ch == '"' and not escape_next:
            in_string = not in_string
            i += 1
            continue

        if not in_string:
            if ch == open_char:
                depth += 1
            elif ch == close_char:
                depth -= 1
                if depth == 0:
                    candidate = text[start:i + 1]
                    parsed = _try_parse(candidate)
                    if parsed is not None:
                        return parsed
                    # 解析失败，继续往后找下一个起始位置
                    next_start = text.find(open_char, i + 1)
                    if next_start != -1:
                        return _extract_balanced(text, next_start, open_char, close_char)
                    return None

        i += 1

    return None
