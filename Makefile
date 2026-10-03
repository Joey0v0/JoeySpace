.PHONY: build run-api run-ws run-push test env-up env-down clean

# 启动中间件环境
env-up:
	cd deploy && docker-compose up -d mysql redis kafka

env-down:
	cd deploy && docker-compose down

# 本地构建
build:
	go build -o bin/im-api   ./cmd/api/
	go build -o bin/im-ws    ./cmd/ws/
	go build -o bin/im-push  ./cmd/push/

# 本地启动三个服务（在不同终端运行）
run-api:
	go run cmd/api/main.go

run-ws:
	go run cmd/ws/main.go

run-push:
	go run cmd/push/main.go

# 运行测试
test:
	go test ./... -v -cover

# Docker 全量启动
docker-up:
	cd deploy && docker-compose up -d --build

docker-down:
	cd deploy && docker-compose down

# 清理构建产物
clean:
	rm -rf bin/
	rm -f im-api im-ws im-push
