"""一次请求、一次响应的包工具，不读取环境或文件。"""
import json
import sys

request = json.load(sys.stdin)
if request.get("protocol") != 1:
    response = {"protocol": 1, "error": "unsupported protocol"}
elif request.get("method") == "describe":
    response = {"protocol": 1, "tools": ["text-stats"], "version": "0.1.0"}
elif request.get("method") == "call" and request.get("tool") == "text-stats":
    text = request["arguments"]["text"]
    result = {"characters": len(text), "nonempty_lines": sum(bool(line.strip()) for line in text.splitlines()), "whitespace_words": len(text.split())}
    response = {"protocol": 1, "result": json.dumps(result, ensure_ascii=False)}
else:
    response = {"protocol": 1, "error": "unknown method or tool"}
print(json.dumps(response, ensure_ascii=False))
