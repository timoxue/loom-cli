#!/usr/bin/env python3
"""
Toy agent that speaks MCP to a local loom sidecar and lets Claude drive.

This is a probe, not a product. Its job is to answer three questions that
only a real agent round-trip can answer:

  1. Does Claude produce well-formed arguments for loom's typed IR schemas?
  2. Can Claude recover from loom's security errors on a subsequent turn?
  3. Is loom's tool-result shape sufficient for an agent to reason about?

Usage:
  export ANTHROPIC_API_KEY=...
  loom serve --skills-dir test_skills &
  python toys/agent.py "please write hello world to out/greeting.txt"

Flow:
  - GET /v1/mcp tools/list                 (discover skills)
  - pass them to anthropic.messages.create (Claude sees them as tools)
  - if Claude emits tool_use → POST tools/call, feed result back as tool_result
  - cap at 3 turns so error-remediation is observable without runaway cost
  - on success, the LAST line printed is the literal commit command

The script deliberately has no retries, no pretty UI, no persistence. Bugs
it surfaces are the product; fix them in serve.go, not here.
"""

import json
import os
import sys
import urllib.request

try:
    from anthropic import Anthropic
except ImportError:
    sys.stderr.write("Install the SDK first:  pip install anthropic\n")
    sys.exit(1)


LOOM_URL = os.environ.get("LOOM_URL", "http://127.0.0.1:8080/v1/mcp")
MODEL = os.environ.get("LOOM_TOY_MODEL", "claude-sonnet-4-6")
MAX_TURNS = 3


def mcp_call(method: str, params: dict | None = None) -> dict:
    """Fire a single JSON-RPC 2.0 request at the loom sidecar."""
    body = json.dumps(
        {"jsonrpc": "2.0", "method": method, "id": 1, "params": params or {}}
    ).encode()
    request = urllib.request.Request(
        LOOM_URL, data=body, headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.loads(response.read())


def discover_tools() -> list[dict]:
    """Ask loom what skills exist and normalize into Anthropic tool shape."""
    envelope = mcp_call("tools/list")
    if "error" in envelope and envelope["error"]:
        sys.exit(f"loom tools/list returned error: {envelope['error']}")
    tools = envelope.get("result", {}).get("tools", [])
    return [
        {
            "name": t["name"],
            "description": t.get("description") or f"Loom skill {t['name']}",
            "input_schema": t.get("inputSchema", {"type": "object", "properties": {}}),
        }
        for t in tools
    ]


def run_tool(name: str, arguments: dict) -> tuple[str, bool, dict]:
    """Call loom, return (text-for-claude, is_error, structured _loom metadata)."""
    str_args = {k: str(v) for k, v in arguments.items()}
    envelope = mcp_call("tools/call", {"name": name, "arguments": str_args})

    if "error" in envelope and envelope["error"]:
        return f"loom protocol error: {envelope['error']}", True, {}

    result = envelope.get("result", {}) or {}
    is_error = bool(result.get("isError"))
    content_blocks = result.get("content", []) or []
    text = "\n".join(
        b.get("text", "") for b in content_blocks if b.get("type") == "text"
    )
    return text, is_error, result.get("_loom", {}) or {}


def main() -> int:
    if len(sys.argv) < 2:
        sys.exit("usage: agent.py <prompt>")
    prompt = sys.argv[1]

    client = Anthropic()
    tools = discover_tools()
    print(f"[toy] loom exposes {len(tools)} tool(s): "
          f"{', '.join(t['name'] for t in tools)}\n")

    messages: list[dict] = [{"role": "user", "content": prompt}]
    last_session_id = ""

    for turn in range(1, MAX_TURNS + 1):
        print(f"[toy] --- turn {turn} ---")
        response = client.messages.create(
            model=MODEL, max_tokens=1024, tools=tools, messages=messages
        )

        # Echo whatever text Claude wants to say.
        for block in response.content:
            if block.type == "text" and block.text.strip():
                print(f"[claude] {block.text.strip()}")

        if response.stop_reason != "tool_use":
            break

        tool_uses = [b for b in response.content if b.type == "tool_use"]
        messages.append({"role": "assistant", "content": response.content})

        tool_results = []
        for use in tool_uses:
            print(f"[toy] dispatching tool_use to loom: {use.name}({dict(use.input)})")
            text, is_error, loom_meta = run_tool(use.name, dict(use.input))
            if loom_meta.get("session_id"):
                last_session_id = loom_meta["session_id"]
            label = "ERROR" if is_error else "OK"
            print(f"[loom:{label}] {text}\n")
            tool_results.append(
                {
                    "type": "tool_result",
                    "tool_use_id": use.id,
                    "content": text,
                    "is_error": is_error,
                }
            )
        messages.append({"role": "user", "content": tool_results})
    else:
        print(f"[toy] hit max_turns={MAX_TURNS} without final response")

    print()
    if last_session_id:
        print(f"SUCCESS: To promote the shadow changes to your real workspace, run:")
        print(f"  loom commit {last_session_id} --yes")
    else:
        print("[toy] no shadow session was produced (no successful tool call).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
