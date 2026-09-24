#!/usr/bin/env python3
"""通过公开 Images 网关请求一次图片，保存图片和不含密钥的对账证据。"""

import argparse
import base64
import getpass
import hashlib
import json
import os
from pathlib import Path
import struct
import time
from datetime import datetime, timezone
import urllib.error
import urllib.parse
import urllib.request
import uuid


class NoRedirect(urllib.request.HTTPRedirectHandler):
    # 生图请求不能因重定向重新发送，也不能把认证头带到其它站点。
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def endpoint_for(base_url):
    parts = urllib.parse.urlsplit(base_url.strip())
    if parts.scheme not in ("http", "https") or not parts.hostname:
        raise ValueError("地址必须是 HTTP(S) URL")
    if parts.username or parts.password or parts.query or parts.fragment:
        raise ValueError("地址不能包含凭据、查询参数或片段")
    if parts.scheme != "https" and parts.hostname not in ("localhost", "127.0.0.1", "::1"):
        raise ValueError("远程测试必须使用 HTTPS")
    path = parts.path.rstrip("/")
    if not path.endswith("/v1"):
        path += "/v1"
    return urllib.parse.urlunsplit((parts.scheme, parts.netloc, path + "/images/generations", "", ""))


def run_once(base_url, api_key, output_dir, model="gpt-image-2", size="3840x2160", timeout=600):
    if not api_key or any(c.isspace() for c in api_key):
        raise ValueError("请提供有效的 API Key")
    endpoint = endpoint_for(base_url)
    client_id = "image-billing-test-" + uuid.uuid4().hex
    output = Path(output_dir).resolve() / client_id
    output.mkdir(parents=True, exist_ok=False)
    payload = {
        "model": model,
        "prompt": "A simple blue ceramic cup on a white table, soft daylight, minimalist still life, no text.",
        "n": 1,
        "size": size,
        "quality": "auto",
        "output_format": "png",
        "stream": False,
    }
    report = {
        "started_at": datetime.now(timezone.utc).isoformat(),
        "endpoint": endpoint,
        "client_request_id": client_id,
        "request": payload,
        "client_attempts": 1,
        "automatic_retries": False,
        "status": "pending",
        "billing_verification": "需要服务端使用记录、扣费幂等记录和余额/配额变化共同确认，HTTP 成功不代表已扣费。",
        "images": [],
    }
    report_path = output / "report.json"

    def save():
        # 报告不保存认证头、图片 base64 或可能携带签名的图片 URL。
        text = json.dumps(report, ensure_ascii=False, indent=2).replace(api_key, "[REDACTED]")
        report_path.write_text(text + "\n", encoding="utf-8")

    save()
    request = urllib.request.Request(endpoint, data=json.dumps(payload).encode(), headers={
        "Authorization": "Bearer " + api_key,
        "Content-Type": "application/json",
        "Accept": "application/json",
        "User-Agent": "TokenRouter-Image-Billing-Test/1.0",
        "X-Client-Request-ID": client_id,
    }, method="POST")
    started = time.monotonic()
    try:
        # 只执行一次 POST；包括超时、429 和 5xx 在内的异常均不重试。
        opener = urllib.request.build_opener(NoRedirect())
        with opener.open(request, timeout=timeout) as response:
            report["http_status"] = response.status
            report["request_id"] = response.headers.get("X-Request-ID")
            limit = 64 * 1024 * 1024
            body = response.read(limit + 1)
            if len(body) > limit:
                raise ValueError("响应超过 64 MiB，请用请求 ID 查询服务端结果，不要直接重复生图")
        report["response_bytes"] = len(body)
        data = json.loads(body)
        if not isinstance(data, dict):
            raise ValueError("上游响应不是 JSON 对象")
        report["usage"] = data.get("usage")
        report["response_id"] = data.get("id")
        if data.get("error"):
            report["error"] = data["error"]
        items = data.get("data") or []
        if not isinstance(items, list):
            raise ValueError("响应 data 不是图片列表")
        for index, item in enumerate(items):
            image = {"index": index}
            if item.get("b64_json"):
                content = base64.b64decode(item["b64_json"], validate=True)
                image["bytes"] = len(content)
                image["sha256"] = hashlib.sha256(content).hexdigest()
                # 仅将符合 PNG 签名的图片保存为 PNG，并从 IHDR 核实实际尺寸。
                if len(content) >= 24 and content.startswith(b"\x89PNG\r\n\x1a\n") and content[12:16] == b"IHDR":
                    image["width"], image["height"] = struct.unpack(">II", content[16:24])
                    target = output / f"image_{index + 1}.png"
                    target.write_bytes(content)
                    image["file"] = str(target)
                else:
                    image["warning"] = "响应含 base64，但不是可核实尺寸的 PNG；未保存文件"
            elif item.get("url"):
                image["url_returned"] = True
                image["warning"] = "响应为图片 URL，测试程序不自动下载"
            report["images"].append(image)
        report["image_count"] = sum(bool(item.get("b64_json") or item.get("url")) for item in items)
        report["status"] = "success" if report["image_count"] == 1 and not data.get("error") else "unexpected_response"
    except urllib.error.HTTPError as exc:
        report["http_status"] = exc.code
        report["request_id"] = exc.headers.get("X-Request-ID")
        report["status"] = "http_error"
        report["error"] = exc.read(16 * 1024).decode("utf-8", errors="replace")
    except Exception as exc:
        # 超时后上游可能仍在执行或已扣费，因此保留不确定状态供管理员对账。
        report["status"] = "outcome_uncertain"
        report["error"] = str(exc)
    finally:
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
        report["finished_at"] = datetime.now(timezone.utc).isoformat()
        save()
    return report_path, report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="站点根地址或以 /v1 结尾的地址")
    parser.add_argument("--model", default="gpt-image-2")
    parser.add_argument("--size", default="3840x2160", help="默认 4K 横图；不会按失败结果自动降级")
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument("--output-dir", default=".dev/reports/image-gateway")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("timeout 必须为正数")
    # 优先读取进程环境；未设置时隐藏输入，不允许把密钥写入命令行参数。
    key = os.environ.get("TOKENROUTER_API_KEY") or getpass.getpass("API Key（输入不回显）: ")
    path, result = run_once(args.base_url, key.strip(), args.output_dir, args.model, args.size, args.timeout)
    print(json.dumps({"report": str(path), "status": result["status"],
                      "request_id": result.get("request_id"), "usage": result.get("usage"),
                      "images": result["images"]}, ensure_ascii=False, indent=2).replace(key, "[REDACTED]"))
    return 0 if result["status"] == "success" else 1


if __name__ == "__main__":
    raise SystemExit(main())
