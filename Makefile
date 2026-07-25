.PHONY: build clean dist

# 构建目标
build:
	mkdir -p dist
	go build -o dist/termcp-relay-$(shell go env GOOS)-$(shell go env GOARCH).exe

# 清理构建文件
clean:
	rm -r dist

# 构建并清理旧文件
dist: clean build
