# Image processing and file forwarding

The optional pipeline describes images for models that cannot accept them.
Configure `processors.vision_models` as an ordered list, or forward images
directly to a vision-capable upstream. `rewrite_vision: off` disables image
rewriting. No PDF parser, OCR service, document downloader or page renderer is
included. Poppler and Ghostscript are not required.

Files stay in their native request format when using a matching upstream API.
Cross-protocol translation preserves inline file data and existing upstream file
IDs. Responses/Anthropic document URLs require native upstream support; conversion
to Chat Completions returns an explicit error. Upstream file type/model limits
still apply. Applications handle extraction, OCR, chunking and retrieval.

Function-tool definitions and model-generated tool calls are forwarded to the
client for execution. This applies to ordinary functions named `web_search` too.
The proxy does not inject search tools, execute searches, perform search retry
loops, or publish a search MCP service.
