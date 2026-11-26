FROM node:22-alpine
WORKDIR /app
ENV SMALLCLOUD_DATA_DIR=/data
RUN mkdir -p /data
COPY server.mjs /app/server.mjs
EXPOSE 8080
CMD ["node", "server.mjs"]
