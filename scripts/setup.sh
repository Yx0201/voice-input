#!/usr/bin/env bash
# 下载本地引擎所需的模型文件到 ~/.voice_input/models/
# 自动检测 HuggingFace 连通性,不通则切换 hf-mirror.com 镜像(国内网络友好)。
set -euo pipefail

BASE_DIR="$HOME/.voice_input"
SENSE_DIR="$BASE_DIR/models/sense-voice"
VAD_DIR="$BASE_DIR/models/vad"

# SenseVoice-Small(int8 量化,约 166MB;支持中/英/日/韩/粤语)
SENSE_REPO="csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
# silero VAD(约 2MB)。主源为 HF 仓库 csukuangfj/vad,备用 GitHub release。
VAD_HF_PATH="csukuangfj/vad/resolve/main/silero_vad.onnx"
VAD_GITHUB="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"

# ---- 镜像选择 ----
HF="${HF_ENDPOINT:-https://huggingface.co}"
if ! curl -sI --max-time 6 -o /dev/null "$HF"; then
  echo "⚠️  直连 $HF 不通,切换到 hf-mirror.com 镜像"
  HF="https://hf-mirror.com"
fi
echo "下载源: $HF"

# fetch <url> <目标路径> <最小合法字节数>
fetch() {
  local url="$1" dest="$2" min="$3"
  if [[ -f "$dest" ]] && (( $(stat -f%z "$dest") > min )); then
    echo "✅ 已存在,跳过: $dest"
    return
  fi
  mkdir -p "$(dirname "$dest")"
  echo "⬇️  下载: $url"
  curl -L --fail --retry 3 --progress-bar -o "$dest.tmp" "$url"
  local size
  size=$(stat -f%z "$dest.tmp")
  if (( size <= min )); then
    rm -f "$dest.tmp"
    echo "❌ 文件不完整($size 字节),请重跑 make setup" >&2
    exit 1
  fi
  mv "$dest.tmp" "$dest"
}

fetch "$HF/$SENSE_REPO/resolve/main/model.int8.onnx" "$SENSE_DIR/model.int8.onnx" 150000000
fetch "$HF/$SENSE_REPO/resolve/main/tokens.txt"      "$SENSE_DIR/tokens.txt"      100000

# VAD:已有则跳过;否则先试 HF(镜像),失败再试 GitHub release
if [[ -s "$VAD_DIR/silero_vad.onnx" ]] && (( $(stat -f%z "$VAD_DIR/silero_vad.onnx" 2>/dev/null || echo 0) > 1500000 )); then
  echo "✅ 已存在,跳过: $VAD_DIR/silero_vad.onnx"
else
  mkdir -p "$VAD_DIR"
  if curl -sL --fail --max-time 180 -o "$VAD_DIR/silero_vad.onnx" "$HF/$VAD_HF_PATH" \
     && (( $(stat -f%z "$VAD_DIR/silero_vad.onnx" 2>/dev/null || echo 0) > 1500000 )); then
    echo "✅ silero_vad.onnx 已下载(HF 源)"
  else
    rm -f "$VAD_DIR/silero_vad.onnx"
    echo "⚠️  HF 源失败,改用 GitHub release"
    fetch "$VAD_GITHUB" "$VAD_DIR/silero_vad.onnx" 1500000
  fi
fi

echo
echo "🎉 模型就绪:"
echo "   $SENSE_DIR/model.int8.onnx ($(du -h "$SENSE_DIR/model.int8.onnx" | cut -f1))"
echo "   $SENSE_DIR/tokens.txt"
echo "   $VAD_DIR/silero_vad.onnx"
echo
echo "下一步: make build && ./voice-input check"
