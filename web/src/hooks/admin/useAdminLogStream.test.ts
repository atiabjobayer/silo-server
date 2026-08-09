import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken } from "@/api/client";
import {
  applyAdminLogAppend,
  applyAdminLogAppends,
  buildAdminLogStreamQuery,
  buildAdminLogStreamUrl,
  useAdminLogStream,
} from "./useAdminLogStream";

describe("buildAdminLogStreamQuery", () => {
  it("serializes only defined log filters", () => {
    expect(
      buildAdminLogStreamQuery({
        request_id: "req-1",
        component: "api",
        playback_session_id: "playback-123",
        q: "",
        limit: 50,
      }),
    ).toBe("request_id=req-1&component=api&playback_session_id=playback-123&limit=50");
  });
});

describe("buildAdminLogStreamUrl", () => {
  it("includes stream, filters, and auth token", () => {
    expect(
      buildAdminLogStreamUrl("app", { request_id: "req-1", component: "api" }, "token-123", {
        protocol: "https:",
        host: "example.com",
      }),
    ).toBe(
      "wss://example.com/api/v1/admin/logs/ws?stream=app&request_id=req-1&component=api&token=token-123",
    );

    expect(
      buildAdminLogStreamUrl(
        "audit",
        { playback_session_id: "playback-123", request_id: "req-9" },
        "token-123",
        {
          protocol: "https:",
          host: "example.com",
        },
      ),
    ).toBe(
      "wss://example.com/api/v1/admin/logs/ws?stream=audit&playback_session_id=playback-123&request_id=req-9&token=token-123",
    );
  });
});

describe("useAdminLogStream", () => {
  const createdUrls: string[] = [];

  class MockWebSocket {
    static instances: MockWebSocket[] = [];
    public onopen?: () => void;
    public onmessage?: (event: MessageEvent) => void;
    public onerror?: () => void;
    public onclose?: () => void;
    public readyState = 0;

    constructor(url: string) {
      createdUrls.push(url);
      MockWebSocket.instances.push(this);
    }

    close() {
      this.readyState = 3;
      this.onclose?.();
    }
  }

  beforeEach(() => {
    createdUrls.length = 0;
    MockWebSocket.instances = [];
    setAccessToken(null);
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", MockWebSocket as unknown as typeof WebSocket);
  });

  afterEach(() => {
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("reconnects when the access token becomes available", () => {
    const { rerender } = renderHook(() => useAdminLogStream("app", {}, true));
    vi.advanceTimersByTime(250);

    expect(createdUrls).toHaveLength(1);
    expect(createdUrls[0]).not.toContain("token=");

    setAccessToken("token-123");
    rerender();
    vi.advanceTimersByTime(250);

    expect(createdUrls).toHaveLength(2);
    expect(createdUrls[1]).toContain("token=token-123");
  });
});

describe("applyAdminLogAppend", () => {
  it("prepends new rows, dedupes by id, and enforces the limit", () => {
    expect(
      applyAdminLogAppend(
        [
          { id: 2, message: "older" },
          { id: 1, message: "oldest" },
        ],
        { id: 3, message: "new" },
        2,
      ),
    ).toEqual([
      { id: 3, message: "new" },
      { id: 2, message: "older" },
    ]);

    expect(
      applyAdminLogAppend(
        [
          { id: 2, message: "older" },
          { id: 1, message: "oldest" },
        ],
        { id: 2, message: "updated" },
        5,
      ),
    ).toEqual([
      { id: 2, message: "updated" },
      { id: 1, message: "oldest" },
    ]);
  });
});

describe("applyAdminLogAppends", () => {
  it("batches appends while keeping newest entries first", () => {
    expect(
      applyAdminLogAppends(
        [
          { id: 3, message: "current newest" },
          { id: 1, message: "oldest" },
        ],
        [
          { id: 4, message: "next" },
          { id: 3, message: "updated current newest" },
          { id: 5, message: "latest" },
        ],
        4,
      ),
    ).toEqual([
      { id: 5, message: "latest" },
      { id: 3, message: "updated current newest" },
      { id: 4, message: "next" },
      { id: 1, message: "oldest" },
    ]);
  });
});
