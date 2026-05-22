# Flow2API Bridge

This repository vendors Flow2API as a git submodule under
`third_party/flow2api`. The submodule should point at the maintained fork branch
`Dimon94/flow2api:dai-api-bridge`.

## Roles

- `new-api` owns user authentication, channel selection, billing, logs, and the
  public `/v1/videos` task API.
- `flow2api` owns Google Labs / Flow account state, ST to AT refresh, captcha
  providers, VideoFX credits, and calls to `aisandbox-pa.googleapis.com/v1`.
- The bridge channel calls Flow2API over HTTP. It does not copy Flow internals
  into the Go process.

## Supported API Surfaces

- `/v1/videos`: async OpenAI-compatible video tasks. The Go gateway owns public
  task IDs, polling, billing, and logs; Flow2API owns the actual Flow task.
- `/v1/images/generations`: synchronous OpenAI-compatible image generation via
  Flow2API `/api/bridge/images`.
- `/v1/images/edits`: best-effort image edit/reference-image flow. Images and
  masks are forwarded to Flow2API as reference images.

## Run With Docker Compose

```bash
git submodule update --init --recursive
docker compose -f docker-compose.yml -f docker-compose.flow2api.yml up -d
```

Create a `Flow2API` channel in the dashboard:

- Type: `Flow2API`
- Base URL: `http://flow2api:8000` in compose, or `http://127.0.0.1:8000` for
  local sidecar development.
- Key: the Flow2API API key from its config/admin page.
- Models: Flow2API model IDs such as `veo_3_1_t2v_fast_landscape`,
  `veo_3_1_i2v_s_fast_fl`, `gemini-3.1-flash-image-landscape`, or
  `gemini-3.0-pro-image-landscape-4k`.

Flow2API tokens, captcha mode, extension route keys, credits, and account
concurrency remain managed in Flow2API.

## Image Models

Flow2API keeps the model mapping. The current bridge exposes the same model IDs
from the submodule:

- `gemini-3.1-flash-image-*`: Flow2API maps this family to `NARWHAL`.
- `gemini-3.0-pro-image-*`: Flow2API maps this family to `GEM_PIX_2`.
- `imagen-4.0-generate-preview-*`: Flow2API Imagen preview image models.

You can call the full concrete model names directly, or use the base aliases
`gemini-3.1-flash-image`, `gemini-3.0-pro-image`, and
`imagen-4.0-generate-preview` with OpenAI `size` / `quality` parameters or
Flow2API `generationConfig` when the client supports them.

## Upstream Sync

Keep Flow2API changes in its own branch:

```bash
cd third_party/flow2api
git fetch upstream
git rebase upstream/main
git push origin dai-api-bridge
cd ../..
git add third_party/flow2api
git commit -m "chore: update flow2api submodule"
```

Do not commit local Flow2API secrets or `config/setting.toml` values.
