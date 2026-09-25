GO := /usr/local/go/bin/go
BINARY := voice-input
APP_NAME := VoiceInput.app

.PHONY: setup tidy build run app clean

# 下载模型文件到 ~/.voice_input/models/
setup:
	bash scripts/setup.sh

# 解析依赖(国内网络建议先: go env -w GOPROXY=https://goproxy.cn,direct)
tidy:
	$(GO) mod tidy

# 构建单二进制。CGO 必开(sherpa-onnx / malgo / 注入 都依赖 cgo)。
# ldflags 把 git 短 hash 烧进二进制——远程排障时日志里的版本指纹是第一线索。
build: tidy
	CGO_ENABLED=1 $(GO) build -ldflags "-X main.buildStamp=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o $(BINARY) ./cmd/voice-input

# 构建 + 环境自检
run: build
	./$(BINARY) check

# 打包成可双击的 macOS 应用:
#   1) 编译二进制放入 .app/Contents/MacOS
#   2) 拷贝 sherpa-onnx 两个 dylib 进 Frameworks 并修正 rpath(脱离 Go 模块缓存)
#   3) ad-hoc 签名,让 TCC 权限记录更稳定
app: build
	@rm -rf $(APP_NAME)
	@mkdir -p $(APP_NAME)/Contents/MacOS $(APP_NAME)/Contents/Frameworks $(APP_NAME)/Contents/Resources
	@cp $(BINARY) $(APP_NAME)/Contents/MacOS/
	@cp packaging/Info.plist $(APP_NAME)/Contents/Info.plist
	@cp packaging/AppIcon.icns $(APP_NAME)/Contents/Resources/AppIcon.icns
	@LIB_DIR=$$(ls -d $$($(GO) env GOMODCACHE)/github.com/k2-fsa/sherpa-onnx-go-macos@*/lib/aarch64-apple-darwin 2>/dev/null | tail -1); \
	if [ -z "$$LIB_DIR" ]; then echo "❌ 找不到 sherpa-onnx 预编译库(先 make build)"; exit 1; fi; \
	cp "$$LIB_DIR/libsherpa-onnx-c-api.dylib" "$$LIB_DIR/libonnxruntime.dylib" $(APP_NAME)/Contents/Frameworks/
	@RPATH=$$(otool -l $(APP_NAME)/Contents/MacOS/$(BINARY) | grep -A2 LC_RPATH | grep 'path ' | awk '{print $$2}' | head -1); \
	if [ -n "$$RPATH" ]; then install_name_tool -delete_rpath "$$RPATH" $(APP_NAME)/Contents/MacOS/$(BINARY); fi; \
	install_name_tool -add_rpath @executable_path/../Frameworks $(APP_NAME)/Contents/MacOS/$(BINARY)
	@bash scripts/make-dev-cert.sh 2>/dev/null || true
	@IDENT=$$(security find-identity -v -p codesigning 2>/dev/null | grep -o '"voiceinput-dev"' | head -1 | tr -d '"'); \
	if [ -n "$$IDENT" ]; then \
		codesign --force --sign "$$IDENT" $(APP_NAME) && \
		echo "✅ 打包完成: $(APP_NAME)(稳定签名: voiceinput-dev;热键 Ctrl+Option+V 切换听写)"; \
	else \
		codesign --force --sign - $(APP_NAME) >/dev/null 2>&1 || true; \
		echo "✅ 打包完成: $(APP_NAME)(⚠️ ad-hoc 签名,TCC 授权会随重编译失效,建议跑 scripts/make-dev-cert.sh)"; \
	fi

# 安装/更新到 /Applications(先停运行中的实例,再替换)
install: app
	@pkill -f "/Applications/$(APP_NAME)/Contents/MacOS" 2>/dev/null; sleep 1; \
	rm -rf "/Applications/$(APP_NAME)" && cp -R $(APP_NAME) /Applications/ && \
	echo "✅ 已安装到 /Applications/$(APP_NAME)(旧实例已停止,可双击启动)"

clean:
	rm -f $(BINARY)
	rm -rf $(APP_NAME)

# 分发包(给不构建的用户):.app + 模型 + 双击安装脚本 → 单个 zip
# 朋友:解压 → 双击 Install.command(装模型,或直接开 app 用内置下载器)→ 双击 VoiceInput.app
DIST_NAME := VoiceInput-dist
dist: app
	@rm -rf $(DIST_NAME) $(DIST_NAME).zip
	@mkdir -p $(DIST_NAME)
	@cp -R $(APP_NAME) $(DIST_NAME)/
	@cp -R $(HOME)/.voice_input/models $(DIST_NAME)/models
	@cp scripts/install-models.command $(DIST_NAME)/Install.command
	@chmod +x $(DIST_NAME)/Install.command
	@zip -qr $(DIST_NAME).zip $(DIST_NAME)
	@echo "✅ 分发包就绪: $(DIST_NAME).zip ($$(du -sh $(DIST_NAME).zip | cut -f1))——发给朋友,解压后先双击 Install.command 再双击 VoiceInput.app"
