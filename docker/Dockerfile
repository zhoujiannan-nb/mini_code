# mini_code 开发环境镜像
# 基于 Ubuntu 22.04，包含完整的 Python 开发环境和常用工具

FROM ubuntu:22.04

# 设置环境变量
ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Asia/Shanghai \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# 安装系统基础工具和开发依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    # 基础工具
    curl \
    wget \
    vim \
    nano \
    git \
    git-lfs \
    openssh-client \
    ca-certificates \
    gnupg \
    lsb-release \
    software-properties-common \
    # 编译工具
    build-essential \
    gcc \
    g++ \
    make \
    cmake \
    # Python 相关
    python3 \
    python3-pip \
    python3-venv \
    python3-dev \
    python3-distutils \
    # 网络工具
    iputils-ping \
    net-tools \
    dnsutils \
    # 文本处理工具
    jq \
    tree \
    less \
    # 其他常用工具
    zip \
    unzip \
    tar \
    gzip \
    # 清理缓存
    && rm -rf /var/lib/apt/lists/* \
    && apt-get clean

# 升级 pip 到最新版本
RUN python3 -m pip install --upgrade pip setuptools wheel

# 复制 requirements.txt 并安装 Python 依赖
COPY ../requirements.txt /tmp/requirements.txt
RUN pip3 install --ignore-installed -r /tmp/requirements.txt && \
    rm -f /tmp/requirements.txt

# 设置 SSH 配置（方便 Git 操作）
RUN mkdir -p /root/.ssh && \
    echo "Host *" > /root/.ssh/config && \
    echo "    StrictHostKeyChecking no" >> /root/.ssh/config && \
    chmod 600 /root/.ssh/config

# 创建工作目录
RUN mkdir -p /app

# 添加启动脚本
RUN echo '#!/bin/bash\n\
echo "========================================"\n\
echo "mini_code 开发环境已就绪"\n\
echo "========================================"\n\
echo "Python 版本: $(python3 --version)"\n\
echo "Pip 版本: $(pip3 --version)"\n\
echo "Git 版本: $(git --version)"\n\
echo ""\n\
echo "可用命令:"\n\
echo "  - python3 mini_code.py          # 启动后台任务"\n\
echo "  - python3 web.py            # 启动Web服务"\n\
echo "  - bash                      # 进入交互式shell"\n\
echo ""\n\
echo "工作目录: /app"\n\
echo "========================================"\n\
exec "$@"' > /entrypoint.sh && \
    chmod +x /entrypoint.sh

# 设置入口点
ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash"]

# 设置工作目录
WORKDIR /app

# 暴露 Web 服务端口
EXPOSE 7500

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD python3 -c "import sys; sys.exit(0)" || exit 1

# 添加标签
LABEL maintainer="mini_code Team" \
      description="mini_code 开发环境镜像 - 基于 Ubuntu 22.04" \
      version="1.0"
