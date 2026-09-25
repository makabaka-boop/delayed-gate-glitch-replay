# 多阶段构建：编译静态 logic 服务
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY internal ./internal
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /logicd ./cmd/logicd

FROM alpine:3.20
RUN adduser -D -u 10001 appuser
COPY --from=build /logicd /usr/local/bin/logicd
USER appuser
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/logicd"]
