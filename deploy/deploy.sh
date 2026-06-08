#!/usr/bin/env bash
#
# deploy.sh — 本地交叉编译 kiro-go，传输二进制到远程，并重启容器。
#
# 前提（首次部署时手动完成一次）：
#   - 已把 deploy/Dockerfile 和 deploy/docker-compose.yml 上传到远程目录
#   - 远程装有 Docker + Compose 插件，当前用户可执行 docker
#   - 本地可免密 ssh/scp 登录远程
#
# 用法：
#   ./deploy/deploy.sh user@host [远程目录]
#
set -euo pipefail

REMOTE="${1:-}"
REMOTE_DIR="${2:-/home/ec2-user/kiro-go}"

if [[ -z "$REMOTE" ]]; then
  echo "用法: $0 user@host [远程目录]" >&2
  exit 1
fi

# 目标平台（服务器是 arm64 就改 GOARCH=arm64）
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "==> 交叉编译 kiro-go ($GOOS/$GOARCH)"
( cd "$PROJECT_ROOT" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -o "$SCRIPT_DIR/kiro-go" . )
trap 'rm -f "$SCRIPT_DIR/kiro-go"' EXIT

echo "==> 传输二进制到 $REMOTE:$REMOTE_DIR"
scp "$SCRIPT_DIR/kiro-go" "$REMOTE:$REMOTE_DIR/kiro-go"

echo "==> 远程重建并重启容器"
ssh "$REMOTE" "cd '$REMOTE_DIR' && docker compose up -d --build && docker compose ps"

echo "==> 完成。面板: https://kiro-aws.liuxinkeji.cn/kiro_admin"
