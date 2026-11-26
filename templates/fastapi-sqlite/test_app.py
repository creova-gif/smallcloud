from __future__ import annotations

import importlib.util
import os
import pathlib
import sqlite3
import tempfile
import types
import unittest
from unittest import mock

HAS_FASTAPI = importlib.util.find_spec("fastapi") is not None
HAS_HTTPX = importlib.util.find_spec("httpx") is not None

if HAS_FASTAPI and HAS_HTTPX:
    from fastapi.testclient import TestClient


def _load_app_module(db_path: str) -> types.ModuleType:
    app_path = pathlib.Path(__file__).resolve().with_name("app.py")
    spec = importlib.util.spec_from_file_location("smallcloud_fastapi_sqlite_app", app_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {app_path}")
    module = importlib.util.module_from_spec(spec)
    with mock.patch.dict(os.environ, {"SMALLCLOUD_SQLITE_PATH": db_path}):
        spec.loader.exec_module(module)
    return module


class TestSQLiteWAL(unittest.TestCase):
    def test_sqlite_wal_and_busy_timeout(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            db_path = os.path.join(tmpdir, "wal_test.db")
            with sqlite3.connect(db_path) as conn:
                mode_row = conn.execute("PRAGMA journal_mode=WAL;").fetchone()
                timeout_row = conn.execute("PRAGMA busy_timeout=5000;").fetchone()
                self.assertIsNotNone(mode_row)
                self.assertEqual(str(mode_row[0]).lower(), "wal")
                self.assertIsNotNone(timeout_row)
                self.assertEqual(int(timeout_row[0]), 5000)


class TestFastAPIApp(unittest.TestCase):
    @unittest.skipUnless(HAS_FASTAPI, "fastapi is not installed")
    def test_init_db_configures_wal_and_schema(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            db_path = os.path.join(tmpdir, "app.db")
            module = _load_app_module(db_path)
            module._init_db()
            with sqlite3.connect(db_path) as conn:
                mode_row = conn.execute("PRAGMA journal_mode;").fetchone()
                table_row = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name='items';"
                ).fetchone()
                self.assertIsNotNone(mode_row)
                self.assertEqual(str(mode_row[0]).lower(), "wal")
                self.assertEqual(table_row, ("items",))

    @unittest.skipUnless(HAS_FASTAPI and HAS_HTTPX, "fastapi or httpx is not installed")
    def test_healthz_and_create_item(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            db_path = os.path.join(tmpdir, "app.db")
            module = _load_app_module(db_path)
            with TestClient(module.app) as client:
                health_res = client.get("/healthz")
                self.assertEqual(health_res.status_code, 200)
                self.assertEqual(health_res.json(), {"status": "ok"})

                create_res = client.post(
                    "/items",
                    json={"org_id": "acme", "name": "widget"},
                )
                self.assertEqual(create_res.status_code, 200)
                body = create_res.json()
                self.assertEqual(body["org_id"], "acme")
                self.assertEqual(body["name"], "widget")
                self.assertGreaterEqual(body["id"], 1)

            with sqlite3.connect(db_path) as conn:
                rows = conn.execute("SELECT id, org_id, name FROM items;").fetchall()
                self.assertEqual(rows, [(body["id"], "acme", "widget")])


if __name__ == "__main__":
    unittest.main()
