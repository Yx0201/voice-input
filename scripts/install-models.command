#!/bin/bash
# 备用模型安装脚本(新版应用可自动下载模型,本脚本仅作离线兜底)。
# 设计原则:非破坏性——已存在完整模型时不做任何删改;只在缺失时复制,
# 复制后必须验证,杜绝"删了旧的、拷不上新的"的删库风险。
set -u

MODELS_SRC="$(cd "$(dirname "$0")/.." && pwd)/models"
DEST_DIR="$HOME/.voice_input/models"

# 必须在解压出的分发包内运行(上级目录有 models/)
if [ ! -f "$MODELS_SRC/sense-voice/model.int8.onnx" ]; then
  echo "❌ 未找到模型文件,请在解压出的 VoiceInput-dist 文件夹内双击本脚本"
  exit 1
fi

# 已装好 → 直接通过
if [ -f "$DEST_DIR/sense-voice/model.int8.onnx" ] && [ -f "$DEST_DIR/sense-voice/tokens.txt" ]; then
  echo "✅ 模型已存在,无需安装"
  exit 0
fi

mkdir -p "$HOME/.voice_input"
# 走到这里说明目标缺失(可能是半截安装):清掉不完整的,重新完整复制
rm -rf "$DEST_DIR"
cp -R "$MODELS_SRC" "$DEST_DIR"

# 复制后验证
if [ -f "$DEST_DIR/sense-voice/model.int8.onnx" ] && [ -f "$DEST_DIR/sense-voice/tokens.txt" ]; then
  echo "✅ 模型安装完成($(du -sh "$DEST_DIR" | cut -f1))"
  echo "现在双击 VoiceInput.app 启动(首次需授权 麦克风 + 辅助功能)"
else
  echo "❌ 安装异常。备选方案:打开 VoiceInput.app,点菜单里的「下载缺失模型」自动下载"
  exit 1
fi
