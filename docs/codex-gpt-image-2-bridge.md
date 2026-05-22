# Codex GPT Image 2 Bridge

The Codex channel exposes `gpt-image-2` on OpenAI-compatible image endpoints:

- `/v1/images/generations`
- `/v1/images/edits`

The gateway rewrites those requests to Codex Responses at
`/backend-api/codex/responses` with an `image_generation` tool, following the
same bridge shape used by `sub2api` and `cockpit-tools`.

## Upstream Request Shape

Codex image requests use:

- main Responses model: `gpt-5.4-mini`
- image tool model: `gpt-image-2`
- tool type: `image_generation`
- tool action: `generate` or `edit`
- upstream streaming enabled, then converted back to an OpenAI Images response

The Codex channel key remains the existing OAuth JSON credential containing
`access_token` and `account_id`.

## Response Mapping

The bridge consumes Codex Responses JSON or SSE events and extracts
`image_generation_call.result`. It returns:

- `b64_json` by default
- a `data:image/...;base64,...` URL when `response_format` is `url`

For edits, multipart images are converted to data URLs and sent as
`input_image` parts. A mask is forwarded as `input_image_mask` when present.
