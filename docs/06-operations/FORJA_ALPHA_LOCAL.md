# Forja Alpha Local Experience

Status: Implemented interface and API preview. Local financial execution is not
yet activated.

## Requirements

- Go 1.26.5
- No Node.js, frontend package manager, model, database, or credentials are
  required to inspect the experience foundation.

## Run

```bash
make alpha-run
```

Open `http://127.0.0.1:8787`.

By default, the application starts in `readiness` mode and truthfully marks
language-model, embedding, ingestion, retrieval, and analytical execution as
unavailable or planned. Configuring both local endpoints changes the mode to
`local-runtime-configured`, but does not probe or validate either endpoint. The
application can create and display a bounded research evidence plan, but it
does not manufacture a financial answer.

## Build

```bash
make alpha-build
./bin/forja-alpha
```

The web interface is embedded in the binary. No static-asset directory or
runtime download is required.

## Local Runtime Configuration

```bash
export FORJA_ALPHA_MODEL_BASE_URL=http://127.0.0.1:8000/v1
export FORJA_ALPHA_EMBEDDING_BASE_URL=http://127.0.0.1:8081/v1
export FORJA_ALPHA_ACCELERATOR='AMD Radeon GPU'
export FORJA_ALPHA_SOFTWARE_STACK='ROCm + vLLM'
make alpha-run
```

The endpoint parser accepts only `localhost` or an explicit loopback IP. This
prevents an accidental remote core-inference configuration. Configuration does
not imply health: the current foundation reports `configured-not-probed` until
the Sprint 10 health, identity, and ROCm evidence adapter is implemented.

The Alpha HTTP interface is supported on loopback only in this foundation.
Non-loopback exposure requires a later authenticated, authorized, and
TLS-protected ingress; setting a public listener without that boundary is not a
supported deployment.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | Interface and core-inference readiness |
| `GET` | `/api/v1/bootstrap` | Product, runtime, universe, and capability state |
| `POST` | `/api/v1/research` | Create a bounded evidence plan |
| `GET` | `/api/v1/research/{id}` | Read an in-process preview session |

The preview service is intentionally ephemeral. Durable conversations, memory,
research artifacts, citations, and restart recovery enter through the governed
Forja stores in Sprints 11-12.

## Validate

```bash
make alpha-test
make validate
```
