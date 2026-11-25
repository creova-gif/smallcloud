"""Sample FastAPI + SQLite micro-SaaS application for SmallCloud."""

from __future__ import annotations

import os
import sqlite3
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from fastapi import FastAPI
from pydantic import BaseModel, Field

DB_PATH = os.environ.get("SMALLCLOUD_SQLITE_PATH", "/data/app.db")


def _init_db() -> None:
    os.makedirs(os.path.dirname(DB_PATH) or ".", exist_ok=True)
    with sqlite3.connect(DB_PATH) as conn:
        conn.execute("PRAGMA journal_mode=WAL;")
        conn.execute(
            """
            CREATE TABLE IF NOT EXISTS items (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                org_id TEXT NOT NULL,
                name TEXT NOT NULL
            );
            """
        )
        conn.commit()


@asynccontextmanager
async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
    _init_db()
    yield


app = FastAPI(title="SmallCloud FastAPI Starter", lifespan=lifespan)


class ItemCreate(BaseModel):
    org_id: str = Field(min_length=2, max_length=63)
    name: str = Field(min_length=1, max_length=120)


class ItemOut(BaseModel):
    id: int
    org_id: str
    name: str


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/items", response_model=ItemOut)
def create_item(payload: ItemCreate) -> ItemOut:
    with sqlite3.connect(DB_PATH) as conn:
        cur = conn.execute(
            "INSERT INTO items (org_id, name) VALUES (?, ?)",
            (payload.org_id, payload.name),
        )
        conn.commit()
        row_id = int(cur.lastrowid or 0)
    return ItemOut(id=row_id, org_id=payload.org_id, name=payload.name)
