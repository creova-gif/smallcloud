FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY main.go ./
RUN go mod init smallcloud-go-starter && CGO_ENABLED=0 GOOS=linux go build -o /out/server ./main.go

FROM alpine:3.20
WORKDIR /app
ENV SMALLCLOUD_DATA_DIR=/data
RUN addgroup -S smallcloud && adduser -S -G smallcloud smallcloud \
    && mkdir -p /data \
    && chown -R smallcloud:smallcloud /data
COPY --from=builder /out/server /app/server
USER smallcloud
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
CMD ["/app/server"]
