FROM node:22-alpine
WORKDIR /app
ENV SMALLCLOUD_DATA_DIR=/data
RUN mkdir -p /data && chown -R node:node /data /app
COPY --chown=node:node server.mjs /app/server.mjs
USER node
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD ["node", "-e", "fetch('http://127.0.0.1:8080/healthz').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"]
CMD ["node", "server.mjs"]
