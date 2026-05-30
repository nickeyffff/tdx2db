# syntax=docker/dockerfile:1
FROM --platform=linux/amd64 golang:1.25-bookworm AS builder

# 启用 non-free 以便安装 unrar，并安装构建所需的工具
RUN sed -i 's/Components: main/Components: main contrib non-free/g' /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends unrar curl ca-certificates git make && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src

# 缓存依赖
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并编译
COPY . .
RUN make build

# 最终运行阶段
FROM --platform=linux/amd64 docker.io/library/debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata && \
    ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    update-ca-certificates && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# 从构建器中拷贝生成的二进制文件
COPY --from=builder /src/tdx2db /tdx2db
RUN chmod +x /tdx2db

ENV TZ=Asia/Shanghai

ENTRYPOINT ["/tdx2db"]
