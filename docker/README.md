# UCode Docker 开发环境

## 📦 镜像说明

基于 Ubuntu 22.04 的完整 Python 开发环境镜像，包含：

### 系统工具
- **基础工具**: curl, wget, vim, nano, git, git-lfs, openssh-client
- **编译工具**: build-essential, gcc, g++, make, cmake
- **网络工具**: ping, net-tools, dnsutils
- **文本处理**: jq, tree, less
- **压缩工具**: zip, unzip, tar, gzip

### Python 环境
- Python 3.10 (Ubuntu 22.04 默认版本)
- pip3 (最新版)
- python3-venv (虚拟环境支持)
- python3-dev (开发头文件)

### Python 开发工具
- ipython (交互式 Python)
- jupyter (Jupyter Notebook)
- pylint (代码检查)
- flake8 (代码风格检查)
- black (代码格式化)
- autopep8 (自动格式化)
- mypy (类型检查)

### 项目依赖
自动安装 `requirements.txt` 中的所有依赖：
- loguru
- aiofiles
- apscheduler
- gitpython
- pydantic
- httpx
- aiohttp
- tiktoken
- flask

## 🚀 快速开始

### 1. 构建镜像

```bash
cd docker
chmod +x build.sh
./build.sh
```

或者直接使用 Docker 命令：

```bash
docker build -t ucode_env:latest -f docker/Dockerfile .
```

### 2. 运行容器

#### 方式一：交互式运行（推荐用于开发）

```bash
docker run -it --rm \
  -v $(pwd):/app \
  -p 7500:7500 \
  ucode_env:latest
```

#### 方式二：后台运行

```bash
# 启动容器
docker run -d --name ucode_dev \
  -v $(pwd):/app \
  -p 7500:7500 \
  ucode_env:latest \
  tail -f /dev/null

# 进入容器
docker exec -it ucode_dev bash
```

#### 方式三：直接运行应用

```bash
# 运行后台任务
docker run -it --rm \
  -v $(pwd):/app \
  -v ~/.ssh:/root/.ssh \
  ucode_env:latest \
  python3 ucode.py

# 运行 Web 服务
docker run -it --rm \
  -v $(pwd):/app \
  -p 7500:7500 \
  ucode_env:latest \
  python3 web.py
```

### 3. 挂载配置和数据

```bash
docker run -it --rm \
  -v $(pwd):/app \
  -v $(pwd)/config:/app/config \
  -v $(pwd)/projects:/app/projects \
  -v $(pwd)/logs:/app/logs \
  -v ~/.ssh:/root/.ssh:ro \
  -p 7500:7500 \
  ucode_env:latest
```

## 📝 使用示例

### 在容器内操作

```bash
# 进入容器后
bash

# 查看 Python 版本
python3 --version

# 查看已安装的包
pip3 list

# 运行 UCode 后台任务
python3 ucode.py

# 运行 Web 服务
python3 web.py

# 手动执行单个项目
python3 ucode.py --run uhr

# 重建项目文档
python3 ucode.py --rebuild uhr

# 使用 IPython
ipython

# 使用 Jupyter
jupyter notebook --ip=0.0.0.0 --port=8888 --no-browser --allow-root
```

### 安装额外工具

```bash
# 在容器内安装其他工具
apt-get update && apt-get install -y your-package

# 或安装 Python 包
pip3 install your-python-package
```

## 🔧 高级用法

### 保存容器修改为新镜像

```bash
# 在容器内安装完需要的工具后
docker commit ucode_dev ucode_env:custom

# 或使用 Dockerfile 扩展
```

### 使用 Docker Compose（可选）

创建 `docker-compose.yml`：

```yaml
version: '3.8'

services:
  ucode-dev:
    image: ucode_env:latest
    container_name: ucode_dev
    volumes:
      - .:/app
      - ~/.ssh:/root/.ssh:ro
    ports:
      - "7500:7500"
    stdin_open: true
    tty: true
    command: bash
```

运行：

```bash
docker-compose up -d
docker exec -it ucode_dev bash
```

### 持久化数据卷

```bash
# 创建数据卷
docker volume create ucode_projects
docker volume create ucode_logs
docker volume create ucode_config

# 使用数据卷
docker run -it --rm \
  -v ucode_projects:/app/projects \
  -v ucode_logs:/app/logs \
  -v ucode_config:/app/config \
  -v $(pwd):/app/code \
  -p 7500:7500 \
  ucode_env:latest
```

## 📂 目录结构

```
/app
├── projects/          # 项目代码和API文档
├── logs/             # 日志文件
├── config/           # 配置文件
├── workspace/        # 工作空间
│   └── tmp/         # 临时文件
├── ucode.py          # 主程序
├── web.py            # Web服务
└── requirements.txt  # Python依赖
```

## ⚙️ 环境变量

```bash
# 设置时区
-e TZ=Asia/Shanghai

# Python 相关
-e PYTHONUNBUFFERED=1
-e PYTHONPATH=/app

# Flask 相关
-e FLASK_APP=web.py
-e FLASK_ENV=production
```

## 🐛 故障排查

### 1. Git SSH 密钥问题

```bash
# 确保挂载了 SSH 密钥
docker run -it --rm \
  -v ~/.ssh:/root/.ssh:ro \
  ucode_env:latest
```

### 2. 权限问题

```bash
# 以 root 用户运行（默认）
docker run -it --rm -u root ucode_env:latest

# 或指定用户
docker run -it --rm -u $(id -u):$(id -g) ucode_env:latest
```

### 3. 网络连接问题

```bash
# 测试网络
docker run -it --rm ucode_env:latest ping baidu.com

# 使用 host 网络模式
docker run -it --rm --network host ucode_env:latest
```

## 📌 注意事项

1. **数据持久化**: 建议挂载宿主机目录或使用数据卷来持久化重要数据
2. **SSH 密钥**: 如需访问私有 Git 仓库，请挂载 SSH 密钥目录
3. **端口映射**: Web 服务默认使用 7500 端口，确保端口未被占用
4. **资源限制**: 可根据需要添加 CPU 和内存限制
5. **镜像大小**: 完整开发环境镜像较大，生产环境可考虑精简版

## 🔄 更新镜像

```bash
# 重新构建镜像
./build.sh

# 或删除旧镜像后重新构建
docker rmi ucode_env:latest
./build.sh
```

## 📞 支持

如有问题，请检查：
- Docker 是否正常运行
- 是否有足够的磁盘空间
- 网络连接是否正常
- 配置文件是否正确
