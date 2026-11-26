import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { createServer } from "./server.mjs";

async function startTestServer() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "smallcloud-node-sqlite-"));
  const server = createServer({ dataDir });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  const baseUrl = `http://127.0.0.1:${address.port}`;
  return {
    baseUrl,
    cleanup: async () => {
      await new Promise((resolve, reject) => server.close((err) => (err ? reject(err) : resolve())));
      fs.rmSync(dataDir, { recursive: true, force: true });
    },
  };
}

test("GET /healthz returns ok", async () => {
  const ctx = await startTestServer();
  try {
    const res = await fetch(`${ctx.baseUrl}/healthz`);
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.deepEqual(body, { status: "ok" });
  } finally {
    await ctx.cleanup();
  }
});

test("POST /items persists and GET /items returns items", async () => {
  const ctx = await startTestServer();
  try {
    const emptyRes = await fetch(`${ctx.baseUrl}/items`);
    assert.equal(emptyRes.status, 200);
    assert.deepEqual(await emptyRes.json(), { items: [] });

    const createRes = await fetch(`${ctx.baseUrl}/items`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ org_id: "org-indie-default", name: "first-item" }),
    });
    assert.equal(createRes.status, 201);
    const created = await createRes.json();
    assert.equal(created.name, "first-item");
    assert.equal(created.org_id, "org-indie-default");
    assert.equal(typeof created.id, "string");
    assert.ok(created.id.length > 0);

    const listRes = await fetch(`${ctx.baseUrl}/items`);
    assert.equal(listRes.status, 200);
    const listed = await listRes.json();
    assert.equal(listed.items.length, 1);
    assert.equal(listed.items[0].id, created.id);
    assert.equal(listed.items[0].name, "first-item");

    const invalidRes = await fetch(`${ctx.baseUrl}/items`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "" }),
    });
    assert.equal(invalidRes.status, 400);
  } finally {
    await ctx.cleanup();
  }
});
