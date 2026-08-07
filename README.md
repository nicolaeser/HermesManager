# Hermes Manager

Hermes Manager is a small Go CLI for installing and managing
[NousResearch Hermes Agent](https://github.com/nousresearch/hermes-agent) with
Docker Compose.

## Requirements

- Linux or macOS
- Docker with Docker Compose

## Install the command

```bash
curl -fsSL https://raw.githubusercontent.com/nicolaeser/HermesManager/main/install.sh | sh
```

For a user-only installation:

```bash
curl -fsSL https://raw.githubusercontent.com/nicolaeser/HermesManager/main/install.sh | sh -s -- --user
```

## Install Hermes

```bash
hermes-manager install /srv/hermes/main
hermes-manager dashboard /srv/hermes/main
```

To prepare an instance without starting it yet (for compose tweaks or
migration), use `--no-start`:

```bash
hermes-manager install --no-start /srv/hermes/main
hermes-manager start /srv/hermes/main
```

In the interactive menu, install and start are separate prompts: confirm install
with `Y`, then answer `y` or `n` for starting immediately.

Hermes listens on `127.0.0.1` by default. Use `--bind-all` only when it should
listen on `0.0.0.0`:

```bash
hermes-manager install --bind-all --dashboard-port 9120 --api-port 8650 /srv/hermes/main
```

Use a separate folder for every additional instance. Host ports are chosen
automatically on new installs: the manager skips ports already listening and
ports reserved by sibling Hermes Manager instances under the same parent
folder. Omit `--dashboard-port` / `--api-port` (or pass `0`) for auto-detect.
Interactive `menu` install shows suggested free ports and lets you override.

## Common commands

```bash
hermes-manager start /srv/hermes/main
hermes-manager stop /srv/hermes/main
hermes-manager restart /srv/hermes/main
hermes-manager status /srv/hermes/main
hermes-manager logs /srv/hermes/main
hermes-manager backup /srv/hermes/main
hermes-manager update /srv/hermes/main
hermes-manager rollback /srv/hermes/main
hermes-manager doctor /srv/hermes/main
hermes-manager menu /srv/hermes/main
hermes-manager self-update
```

Run `hermes-manager help` for the complete command list.

## Instance files

```text
/srv/hermes/main/
├── docker-compose.yml
├── docker-compose.override.yml   # optional user customizations
├── .manager/
├── data/                 # HERMES_HOME → /opt/data (critical)
│   └── workspace/        # created by Hermes Agent itself
├── workspace/            # optional project files → /workspace
└── backups/              # hermes backup/import archives
```

| Host path | Container | Role |
|-----------|-----------|------|
| `data/` | `/opt/data` | Official Hermes state (config, sessions, memories, agent workspace). **Must keep for updates.** |
| `workspace/` | `/workspace` | Optional project directory. Not Hermes core state; often empty. |
| `backups/` | `/backups` | Host archives for `hermes backup` / import. |

### Custom volumes

Put extra mounts in `docker-compose.override.yml` (recommended). The manager
merges that file on every compose command and never regenerates it:

```yaml
services:
  hermes:
    volumes:
      - /home/user/.openclaw:/openclaw:ro
```

Edits to `docker-compose.yml` itself are also kept on `start` / `update` by
default (only image, ports, and a few managed fields are synced).

Rebuild policy is per instance:

```bash
# set at install (or re-run install with the flag to change it)
hermes-manager install --rebuild-on-start /srv/hermes/main

# one-shot force rebuild on this start only
hermes-manager start --rebuild /srv/hermes/main
```

Without `--rebuild`, start follows `rebuild_compose_on_start` in
`.manager/instance.json` (default: false = preserve custom compose edits).
Full rewrite also happens on `install` repair.

Hermes executables live in the image. **Update only recreates the container**
(bind mounts stay). The manager never runs `docker compose down -v` and never
deletes `data/` during update.

## Safety

- Dashboard and API ports are localhost-only unless `--bind-all` is explicit.
- Only `data`, `workspace`, and `backups` are mounted into the container.
- Backups and manager credentials are stored with owner-only permissions.
- Instance exports contain dashboard credentials and must be stored securely.
- Report vulnerabilities through GitHub private vulnerability reporting.

## Build from source

```bash
make check
make build
```

The binary is written to `build/hermes-manager`.
