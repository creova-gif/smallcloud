import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { pathToFileURL } from "node:url";

const MAX_BODY_BYTES = 64 * 1024;

function sendJson(res, statusCode, payload) {
  const body = JSON.stringify(payload);
  res.writeHead(statusCode, {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(body),
  });
  res.end(body);
}

function readJsonBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;
    req.on("data", (chunk) => {
      total += chunk.length;
      if (total > MAX_BODY_BYTES) {
        reject(Object.assign(new Error("request body too large"), { statusCode: 413 }));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => {
      try {
        const raw = Buffer.concat(chunks).toString("utf8");
        resolve(raw.length === 0 ? {} : JSON.parse(raw));
      } catch {
        reject(Object.assign(new Error("invalid json body"), { statusCode: 400 }));
      }
    });
    req.on("error", (err) => reject(err));
  });
}

function readItems(storePath) {
  try {
    const content = fs.readFileSync(storePath, "utf8");
    const items = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (trimmed.length > 0) {
        items.push(JSON.parse(trimmed));
      }
    }
    return items;
  } catch (err) {
    if (err && err.code === "ENOENT") {
      return [];
    }
    throw err;
  }
}

export function createServer(options = {}) {
  const dataDir = options.dataDir || process.env.SMALLCLOUD_DATA_DIR || "/data";
  const storePath = path.join(dataDir, "items.ndjson");
  let writeQueue = Promise.resolve();

  function appendItem(item) {
    const next = writeQueue.then(() => {
      fs.mkdirSync(dataDir, { recursive: true });
      fs.appendFileSync(storePath, JSON.stringify(item) + "\n", "utf8");
      return item;
    });
    writeQueue = next.catch(() => {});
    return next;
  }

  return http.createServer(async (req, res) => {
    const url = new URL(req.url || "/", "http://localhost");

    if (req.method === "GET" && url.pathname === "/healthz") {
      sendJson(res, 200, { status: "ok" });
      return;
    }

    if (req.method === "GET" && url.pathname === "/items") {
      try {
        const items = readItems(storePath);
        sendJson(res, 200, { items });
      } catch {
        sendJson(res, 500, { error: "failed to read items" });
      }
      return;
    }

    if (req.method === "POST" && url.pathname === "/items") {
      try {
        const body = await readJsonBody(req);
        if (!body || typeof body !== "object" || typeof body.name !== "string" || body.name.trim().length === 0) {
          sendJson(res, 400, { error: "name must be a non-empty string" });
          return;
        }
        const item = {
          id: randomUUID(),
          org_id: typeof body.org_id === "string" && body.org_id.trim().length > 0 ? body.org_id.trim() : "org-indie-default",
          name: body.name.trim(),
          created_at: new Date().toISOString(),
        };
        await appendItem(item);
        sendJson(res, 201, item);
      } catch (err) {
        const statusCode = err && typeof err.statusCode === "number" ? err.statusCode : 500;
        const message = err instanceof Error ? err.message : "internal error";
        sendJson(res, statusCode, { error: message });
      }
      return;
    }

    sendJson(res, 404, { error: "not found" });
  });
}

const isEntrypoint = process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url;

if (isEntrypoint) {
  const port = Number.parseInt(process.env.PORT || "8080", 10);
  const server = createServer();
  server.listen(port, "0.0.0.0");
}
