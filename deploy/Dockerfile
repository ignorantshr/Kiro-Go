# 预编译部署：直接 COPY 本地交叉编译好的 kiro-go 二进制，镜像内不含 Go 工具链、源码或 web/。
# 二进制已用 go:embed 打包了 web 静态资源，因此运行时自包含。
FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY kiro-go .
RUN mkdir -p /app/data

EXPOSE 8080
VOLUME /app/data

CMD ["./kiro-go"]
