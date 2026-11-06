# go-llm-proxy

A single-binary LLM proxy for local and upstream models. Routes requests, translates API protocols, forwards client-owned tool calls, and provides a unified model/key/usage console.

**[Landing page](https://go-llm-proxy.com)** · **[Config generator](https://go-llm-proxy.com/configure.html)** · **[Releases](https://github.com/yatesdr/go-llm-proxy/releases)**

## Common use cases

Use one local endpoint for your applications and coding agents, with per-key model access and upstream credentials held on the server. Document parsing, RAG and web search belong to the application or agent.

## What it does

- **Protocol translation** — Claude Code speaks Anthropic Messages. Codex speaks OpenAI Responses. Your vLLM speaks Chat Completions. Anthropic-compatible providers speak Messages. AWS Bedrock speaks Converse with SigV4. The proxy translates between them automatically in either direction.
- **Model multiplexing** — Aggregate local GPU servers, cloud APIs, and third-party providers behind one endpoint. Clients see one model list.
- **API key management** — Issue proxy keys with per-key model restrictions. Backend credentials stay on the server.
- **Vision pipeline** — Images sent to text-only models are described by a vision-capable model and replaced with text. Transparent to the client.
- **Audio routing** — Dedicated, load-balanced Whisper transcription/translation and text-to-speech routes, including TTS voice discovery for compatible backends such as Kokoro-FastAPI.
- **Web console** — Unified administration and usage overview at `/admin`. Local HTML/CSS/JS assets embedded in the binary; no frontend build required. [Console guide](docs/web-ui.md).
- **Usage monitoring** — Daily-aggregated statistics in SQLite. CLI reports and per-user/model breakdowns in the admin overview.
- **Config generator** — Built-in web UI creates ready-to-use configs for Claude Code, Codex, OpenCode, and Qwen Code. Also available [standalone](https://go-llm-proxy.com/configure.html).
- **Context window detection** — Auto-queries backends at startup. Manual override per model.
- **Hot reload** — Config reloads on file save or SIGHUP. Add models or rotate keys without restarting.
- **Security** — Constant-time auth, IP rate limiting, SSRF protection, sanitized error responses, path allowlisting.

## Quick start

```bash
./go-llm-proxy -config config.yaml
```

Or with Docker:

```bash
mkdir -p config
cp config.yaml.example config/config.yaml
cp docker/.env.example docker/.env
# edit docker/.env — set GO_LLM_ADMIN_PASSWORD
docker compose -f docker/docker-compose.yml up -d
```

Boots with a placeholder model/key so `docker compose ps` shows healthy
immediately, and the admin console at `/admin` is already usable with the
password from `.env` — no YAML editing required to log in and replace the
placeholders through the UI. Full walkthrough (first login, enabling the
usage dashboard, telemetry retention): [docs/docker.md](docs/docker.md).

## Minimum config

```yaml
listen: ":8080"

models:
  - name: my-model
    backends:
      - url: http://192.168.1.10:8000/v1

keys:
  - key: sk-your-secret-key
    name: admin
```

See [config.yaml.example](config.yaml.example) for a fully annotated starter config with all options.

## Compatibility matrix

What works with each coding assistant through the proxy.

**Protocol**

|  | Claude Code | Codex CLI | OpenCode | Qwen Code |
|---|:---:|:---:|:---:|:---:|
| Native API | Anthropic Messages | OpenAI Responses | Chat Completions | Chat Completions |
| Translation | auto-translated | auto-translated | passthrough | passthrough |

**Core features**

|  | Claude Code | Codex CLI | OpenCode | Qwen Code |
|---|:---:|:---:|:---:|:---:|
| Text + streaming | ✓ | ✓ | ✓ | ✓ |
| Tool calling | ✓ | ✓ | ✓ | ✓ |
| Multi-turn tool loops | ✓ | ✓ | ✓ | ✓ |
| Reasoning display | ✓ | ✓ | — | — |
| Extended thinking | ✓ | ✓ | — | — |

**Proxy-side processing** ([details](docs/pipeline.md))

|  | Claude Code | Codex CLI | OpenCode | Qwen Code |
|---|:---:|:---:|:---:|:---:|
| Image description | ✓ vision | ✓ vision | ✓ vision | ✓ vision |
| Context compaction | — | ✓ | — | — |
| Usage logging & reports | ✓ | ✓ | ✓ | ✓ |

Each assistant speaks a different API protocol. The proxy detects this and translates automatically — no per-model configuration needed for the common case.

## Optional image helpers

Models that cannot accept images may use a configured vision model to describe
images. Vision-capable backends can receive images directly. Files remain opaque:
inline file data and supported file references are forwarded to the upstream.
Use application-side parsing/OCR when your upstream cannot read the document.

```yaml
processors:
  vision_models: [my-vision-model]
```

Proxy-executed web search, the search MCP service, Qdrant proxy, document
extraction/OCR and self-service enrollment have been removed. Client-owned
function calls continue to work across supported API protocols.

## Documentation

| Topic | Link |
|---|---|
| Configuration reference | [docs/config-reference.md](docs/config-reference.md) |
| Claude Code | [docs/claude-code.md](docs/claude-code.md) |
| Codex CLI | [docs/codex.md](docs/codex.md) |
| OpenCode | [docs/opencode.md](docs/opencode.md) |
| Qwen Code | [docs/qwen-code.md](docs/qwen-code.md) |
| Processing pipeline | [docs/pipeline.md](docs/pipeline.md) |
| Docker deployment | [docs/docker.md](docs/docker.md) |
| Production deployment | [docs/deployment.md](docs/deployment.md) |
| Usage monitoring | [docs/usage.md](docs/usage.md) |
| Security | [docs/security.md](docs/security.md) |
