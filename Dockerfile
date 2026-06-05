# builder 阶段默认运行在当前构建机平台；在 buildx 场景下仍可通过 TARGETOS/TARGETARCH 交叉编译目标平台二进制
FROM golang:1.23-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-$(go env GOOS)} GOARCH=${TARGETARCH:-$(go env GOARCH)} go build -o kiro-go .

FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/kiro-go .
COPY --from=builder /app/web ./web
RUN mkdir -p /app/data

EXPOSE 8080
VOLUME /app/data

CMD ["./kiro-go"]
