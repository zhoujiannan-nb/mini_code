"""日志配置工具"""
from loguru import logger
import sys
from pathlib import Path


def setup_logger(project: str = None, branch: str = None):
    """
    配置日志系统
    
    Args:
        project: 项目名称，如果提供则使用项目专属日志文件
        branch: 分支名称，与project一起构成目录名
        
    Returns:
        配置好的logger实例
    """
    # 移除默认的handler
    logger.remove()
    
    if project and branch:
        log_dir = Path(f"projects/{project}-{branch}/logs")
        log_dir.mkdir(parents=True, exist_ok=True)
        log_file = log_dir / "mini_code.log"
    else:
        log_file = "uclaw.log"
    
    # 添加文件日志
    logger.add(
        log_file,
        rotation="10 MB",
        retention="7 days",
        level="INFO",
        format="{time:YYYY-MM-DD HH:mm:ss} | {level: <8} | {message}",
        encoding="utf-8"
    )
    
    # 添加控制台输出
    logger.add(
        sys.stderr,
        level="INFO",
        format="<green>{time:YYYY-MM-DD HH:mm:ss}</green> | <level>{level: <8}</level> | <cyan>{message}</cyan>"
    )
    
    return logger
