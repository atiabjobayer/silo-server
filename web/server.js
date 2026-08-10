import http from "node:http";
import fs from "node:fs";
import path from "node:path";

const PORT = parseInt(process.env.PORT || "4173", 10);
const DIST_DIR = path.resolve("dist");
const API_TARGET = process.env.API_TARGET || "http://silo:8080";

const MIME = {
  ".html": "text/html",
  ".js": "application/javascript",
  ".css": "text/css",
  ".json": "application/json",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".svg": "image/svg+xml",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".wasm": "application/wasm",
};

function serveStatic(res, filePath) {
  const ext = path.extname(filePath).toLowerCase();
  const contentType = MIME[ext] || "application/octet-stream";

  fs.readFile(filePath, (err, data) => {
    if (err) {
      res.writeHead(404, { "Content-Type": "text/plain" });
      res.end("Not Found");
      return;
    }
    res.writeHead(200, { "Content-Type": contentType });
    res.end(data);
  });
}

function serveIndex(res) {
  const indexPath = path.join(DIST_DIR, "index.html");
  fs.readFile(indexPath, (err, data) => {
    if (err) {
      res.writeHead(404, { "Content-Type": "text/plain" });
      res.end("Not Found");
      return;
    }
    res.writeHead(200, { "Content-Type": "text/html" });
    res.end(data);
  });
}

function proxyRequest(req, res, targetUrl) {
  const url = new URL(req.url || "/", targetUrl);
  const options = {
    hostname: url.hostname,
    port: url.port,
    path: url.pathname + url.search,
    method: req.method,
    headers: { ...req.headers },
  };
  // Remove hop-by-hop headers
  delete options.headers?.host;
  delete options.headers?.connection;

  const proxyReq = http.request(options, (proxyRes) => {
    res.writeHead(proxyRes.statusCode || 500, proxyRes.headers);
    proxyRes.pipe(res);
  });

  proxyReq.on("error", () => {
    if (!res.headersSent) {
      res.writeHead(502, { "Content-Type": "text/plain" });
    }
    res.end("Bad Gateway");
  });

  req.pipe(proxyReq);
}

const server = http.createServer((req, res) => {
  const urlPath = req.url || "/";

  // WebSocket upgrade requests are handled exclusively by the "upgrade"
  // event below — do not send an HTTP response here.
  if (req.headers.upgrade?.toLowerCase() === "websocket") {
    return;
  }

  // Proxy API and auth requests to the Go backend.
  if (urlPath.startsWith("/api/") || urlPath.startsWith("/auth/")) {
    proxyRequest(req, res, API_TARGET);
    return;
  }

  // Try to serve a static file.
  let filePath = path.join(DIST_DIR, urlPath === "/" ? "index.html" : urlPath);
  // Normalize to prevent directory traversal.
  filePath = path.resolve(DIST_DIR, path.normalize(filePath));
  if (!filePath.startsWith(DIST_DIR)) {
    res.writeHead(403);
    res.end("Forbidden");
    return;
  }

  fs.stat(filePath, (err, stats) => {
    if (err || !stats.isFile()) {
      // SPA fallback: serve index.html for client-side routing.
      serveIndex(res);
      return;
    }
    serveStatic(res, filePath);
  });
});

// Handle WebSocket upgrades for /api/ paths.
server.on("upgrade", (req, socket, head) => {
  const urlPath = req.url || "";

  if (!urlPath.startsWith("/api/")) {
    socket.destroy();
    return;
  }

  const url = new URL(urlPath, API_TARGET);
  const options = {
    hostname: url.hostname,
    port: url.port,
    path: url.pathname + url.search,
    method: "GET",
    headers: {
      ...req.headers,
      // Keep Upgrade + Connection for the WebSocket handshake; drop host so
      // the backend sees its own name.
      host: undefined,
    },
  };

  const proxyReq = http.request(options);
  proxyReq.on("error", () => socket.destroy());

  proxyReq.on("upgrade", (proxyRes, proxySocket, proxyHead) => {
    // Write the 101 Switching Protocols response back to the client.
    socket.write(
      `HTTP/${proxyRes.httpVersion} ${proxyRes.statusCode} ${proxyRes.statusMessage}\r\n`,
    );
    for (const [key, value] of Object.entries(proxyRes.headers)) {
      if (key && value) {
        socket.write(`${key}: ${Array.isArray(value) ? value.join(", ") : value}\r\n`);
      }
    }
    socket.write("\r\n");

    // Forward any early data from the proxy to the client.
    if (proxyHead.length > 0) {
      socket.write(proxyHead);
    }
    // Forward any early data from the client to the proxy.
    if (head.length > 0) {
      proxySocket.write(head);
    }

    // Bidirectional pipe.
    socket.pipe(proxySocket);
    proxySocket.pipe(socket);
  });

  proxyReq.end();
});

server.listen(PORT, () => {
  console.log(`Silo web proxy listening on :${PORT} → ${API_TARGET}`);
});
