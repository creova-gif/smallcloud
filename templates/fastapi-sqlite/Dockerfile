FROM python:3.12-slim AS base
WORKDIR /app
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    SMALLCLOUD_SQLITE_PATH=/data/app.db

RUN pip install --no-cache-dir fastapi==0.115.0 uvicorn==0.30.6 pydantic==2.9.2 \
    && groupadd --system smallcloud \
    && useradd --system --gid smallcloud --no-create-home smallcloud \
    && mkdir -p /data \
    && chown -R smallcloud:smallcloud /data /app

COPY --chown=smallcloud:smallcloud app.py /app/app.py
USER smallcloud
EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/healthz')"]
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
