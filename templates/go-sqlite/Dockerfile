FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY main.go ./
RUN go mod init smallcloud-go-starter && CGO_ENABLED=0 GOOS=linux go build -o /out/server ./main.go

FROM alpine:3.20
WORKDIR /app
ENV SMALLCLOUD_DATA_DIR=/data
RUN mkdir -p /data
COPY --from=builder /out/server /app/server
EXPOSE 8080
CMD ["/app/server"]
