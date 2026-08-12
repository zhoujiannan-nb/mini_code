#!/bin/bash
# mini_code 环境镜像构建脚本

IMAGE_NAME="mini_code_env"
IMAGE_TAG="latest"

echo "=========================================="
echo "构建 mini_code 开发环境镜像"
echo "=========================================="
echo "镜像名称: ${IMAGE_NAME}"
echo "镜像标签: ${IMAGE_TAG}"
echo ""

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "项目目录: ${PROJECT_DIR}"
echo ""

# 构建镜像
echo "开始构建镜像..."
docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" \
    -f "${SCRIPT_DIR}/Dockerfile" \
    "${PROJECT_DIR}"

if [ $? -eq 0 ]; then
    echo ""
    echo "=========================================="
    echo "✅ 镜像构建成功！"
    echo "=========================================="
    echo ""
    echo "镜像信息:"
    docker images | grep "${IMAGE_NAME}"
    echo ""
    echo "使用示例:"
    echo "  # 交互式运行（挂载代码目录）"
    echo "  docker run -it --rm \\"
    echo "    -v ${PROJECT_DIR}:/app \\"
    echo "    -p 7500:7500 \\"
    echo "    ${IMAGE_NAME}:${IMAGE_TAG}"
    echo ""
    echo "  # 后台运行并进入容器"
    echo "  docker run -d --name mini_code_dev \\"
    echo "    -v ${PROJECT_DIR}:/app \\"
    echo "    -p 7500:7500 \\"
    echo "    ${IMAGE_NAME}:${IMAGE_TAG} \\"
    echo "    tail -f /dev/null"
    echo ""
    echo "  # 进入已运行的容器"
    echo "  docker exec -it mini_code_dev bash"
    echo ""
else
    echo ""
    echo "❌ 镜像构建失败！"
    exit 1
fi
