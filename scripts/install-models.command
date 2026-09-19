#!/bin/bash
# 备用模型安装脚本(新版应用可自动下载模型,本脚本为离线兜底/一键配齐)。
# 设计原则:非破坏性、支持升级——按模型族逐一检查,已存在跳过,缺失才复制;
# 复制后验证。旧版用户(只有整句模型)重跑本脚本即可补齐流式模型。
set -u

MODELS_SRC="$(cd "$(dirname "$0")" && pwd)/models"
DEST_DIR="$HOME/.voice_input/models"

if [ ! -d "$MODELS_SRC" ]; then
  echo "❌ 未找到模型文件,请在解压出的 VoiceInput-dist 文件夹内双击本脚本"
  exit 1
fi
mkdir -p "$DEST_DIR"

FAILED=0
copy_if_missing() { # $1=模型族目录名 $2=标志性文件
  if [ -f "$DEST_DIR/$1/$2" ]; then
    echo "✅ $1 已存在,跳过"
  elif [ -d "$MODELS_SRC/$1" ]; then
    rm -rf "$DEST_DIR/$1" # 清理可能的半截安装
    cp -R "$MODELS_SRC/$1" "$DEST_DIR/$1"
    if [ -f "$DEST_DIR/$1/$2" ]; then
      echo "✅ $1 已安装($(du -sh "$DEST_DIR/$1" | cut -f1))"
    else
      echo "❌ $1 安装异常"
      FAILED=1
    fi
  fi
}

copy_if_missing sense-voice         model.int8.onnx    # 整句引擎
copy_if_missing streaming-paraformer encoder.int8.onnx # 流式引擎
copy_if_missing vad                 silero_vad.onnx    # 断句(整句模式)

if [ "$FAILED" -eq 0 ]; then
  echo "🎉 模型全部就绪——双击 VoiceInput.app 启动"
  echo "   首次需授权:麦克风 + 辅助功能;云端引擎:菜单「切换引擎→云端」输入 Key"
else
  echo "备选方案:打开 VoiceInput.app,点菜单「⬇️ 下载缺失模型」自动下载"
  exit 1
fi
