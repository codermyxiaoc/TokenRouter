// 验证创作台模型候选读取与管理员设置提交的请求契约。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AxiosInstance, InternalAxiosRequestConfig } from "axios";

import { apiClient } from "@/api/client";
import {
  getCreativeModelCandidates,
  getCreativeWorkerStatus,
  updateSettings,
} from "@/api/admin/settings";

function jsonResponse(data: unknown) {
  return {
    status: 200,
    data: { code: 0, message: "ok", data },
    headers: {},
    config: {},
    statusText: "OK",
  };
}

describe("creative model settings API", () => {
  let adapter: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    adapter = vi.fn();
    apiClient.defaults.adapter = adapter as AxiosInstance["defaults"]["adapter"];
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("读取管理员候选并保留候选能力字段", async () => {
    const candidates = [{
      group_id: 12,
      group_name: "Images",
      platform: "grok",
      model: "grok-imagine",
      operations: ["generate", "edit"],
    }];
    adapter.mockResolvedValue(jsonResponse(candidates));

    await expect(getCreativeModelCandidates()).resolves.toEqual(candidates);
    const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig;
    expect(config.method).toBe("get");
    expect(config.url).toBe("/admin/settings/creative-model-candidates");
  });

  it("读取创作台 worker 池状态快照", async () => {
    const status = { running: true, worker_count: 128, busy_workers: 60 };
    adapter.mockResolvedValue(jsonResponse(status));

    await expect(getCreativeWorkerStatus()).resolves.toEqual(status);
    const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig;
    expect(config.method).toBe("get");
    expect(config.url).toBe("/admin/settings/creative-worker-status");
  });

  it("保存创作台模型配置字段", async () => {
    const settings = {
      creative_model_settings: [{
        group_id: 12,
        model: "gpt-image-2",
        operations: ["generate", "inpaint"],
      }],
    };
    adapter.mockResolvedValue(jsonResponse(settings));

    await expect(updateSettings(settings)).resolves.toEqual(settings);
    const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig;
    expect(config.method).toBe("put");
    expect(config.url).toBe("/admin/settings");
    expect(JSON.parse(String(config.data))).toEqual(settings);
  });
});
