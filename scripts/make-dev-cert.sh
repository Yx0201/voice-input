#!/usr/bin/env bash
# 一次性创建本地代码签名证书 voiceinput-dev(10 年有效期)。
# 作用:让 make app 产出的应用拥有稳定签名身份 ——
#   辅助功能/麦克风等 TCC 授权绑定签名身份,ad-hoc 签名每次编译都变,
#   会导致"设置里开关已打开但系统判定未授权";稳定证书根治此问题。
# 已存在同名身份时直接跳过(幂等)。
set -euo pipefail

IDENTITY="voiceinput-dev"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if security find-identity -v -p codesigning 2>/dev/null | grep -q "\"$IDENTITY\""; then
  echo "✅ 身份 $IDENTITY 已存在,无需创建"
  exit 0
fi

openssl req -newkey rsa:2048 -nodes -keyout "$TMP/k.pem" \
  -x509 -days 3650 -out "$TMP/c.pem" \
  -subj "/CN=$IDENTITY" \
  -addext "keyUsage=critical,digitalSignature" \
  -addext "extendedKeyUsage=critical,codeSigning" \
  -addext "basicConstraints=critical,CA:FALSE" 2>/dev/null

# macOS 的 security 只认旧版 PKCS12 加密算法(-legacy;旧 openssl 用后一组参数)
openssl pkcs12 -export -legacy -out "$TMP/i.p12" -inkey "$TMP/k.pem" -in "$TMP/c.pem" \
  -name "$IDENTITY" -passout pass:"$IDENTITY" 2>/dev/null \
|| openssl pkcs12 -export -out "$TMP/i.p12" -inkey "$TMP/k.pem" -in "$TMP/c.pem" \
  -name "$IDENTITY" -passout pass:"$IDENTITY" \
  -certpbe PBE-SHA1-3DES -keypbe PBE-SHA1-3DES -macalg sha1

security import "$TMP/i.p12" -k "$KEYCHAIN" -P "$IDENTITY" -T /usr/bin/codesign
# 添加 codeSign 信任;可能弹出管理员密码框,需要用户确认
security add-trusted-cert -p codeSign -k "$KEYCHAIN" "$TMP/c.pem"

if security find-identity -v -p codesigning | grep -q "\"$IDENTITY\""; then
  echo "✅ 签名身份 $IDENTITY 创建成功"
else
  echo "❌ 创建未生效,请检查上方输出(信任步骤可能被取消)" >&2
  exit 1
fi
