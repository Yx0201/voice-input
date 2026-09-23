#!/bin/bash
# 一键安装/升级脚本:①替换 /Applications 里的 VoiceInput.app 为包内最新版
# (停旧进程→删旧→拷新);②补齐缺失模型(离线兜底,已存在跳过——旧版
# 用户重跑即可补齐流式模型)。应用内亦可菜单「⬇️ 下载缺失模型」在线拉取。
set -u

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
MODELS_SRC="$SRC_DIR/models"

# --- 应用替换:老版本用户重跑本脚本即升级到包内最新版 ---
APP_SRC="$SRC_DIR/VoiceInput.app"
if [ -d "$APP_SRC" ]; then
  pkill -f "/Applications/VoiceInput.app/Contents/MacOS" 2>/dev/null
  sleep 1
  rm -rf /Applications/VoiceInput.app
  cp -R "$APP_SRC" /Applications/
  if [ -d "/Applications/VoiceInput.app/Contents/MacOS" ]; then
    echo "✅ VoiceInput.app 已更新到 /Applications(旧实例已停止)"
  else
    echo "❌ 应用复制异常,请手动拖入 /Applications"
    exit 1
  fi
fi
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
  echo "🎉 模型全部就绪,应用已是最新——双击 /Applications/VoiceInput.app 启动"
  echo "   首次需授权:麦克风 + 辅助功能;云端引擎:菜单「切换引擎→云端」输入 Key"
else
  echo "备选方案:打开 VoiceInput.app,点菜单「⬇️ 下载缺失模型」自动下载"
  exit 1
fi
