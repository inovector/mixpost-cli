<p align="center">
  <img src="art/cover.png" alt="Mixpost CLI" width="900">
</p>

# About Mixpost CLI

Your whole social media workflow, one command away.

Mixpost CLI brings [Mixpost](https://mixpost.app) to the place you already work.
Draft, schedule and review your posts without opening a browser tab, on your own
self-hosted Pro or Enterprise instance.

It also works beautifully with AI assistants like Claude, ChatGPT or Cursor. Ask
for three posts about your new feature scheduled for next week, and your agent
can do it end to end.

## Install

**Homebrew**

```bash
brew install inovector/tap/mixpost
```

**Binary release**

Download the archive for your platform from the
[releases page](https://github.com/inovector/mixpost-cli/releases) and put
`mixpost` on your `PATH`.

**Go**

```bash
go install github.com/inovector/mixpost-cli/cmd/mixpost@latest
```

**Docker**

```bash
docker run --rm \
  -e MIXPOST_URL -e MIXPOST_TOKEN \
  ghcr.io/inovector/mixpost-cli:latest post list -w acme
```

**From source**

```bash
git clone https://github.com/inovector/mixpost-cli && cd mixpost-cli
make build && ./mixpost version
```

## Quick start

Generate a token from **Access Tokens** in the user menu of your Mixpost
dashboard, then:

```bash
mixpost auth login --url https://example.com/mixpost --token <token>
mixpost workspace list                       # find your workspace UUID
mixpost config set workspace <workspace-uuid>

mixpost account list
mixpost post create --text "Hello from the CLI" --account twitter --now
```

The URL is the instance address including the Mixpost core path. `example.com`
is expanded to `https://example.com/mixpost`, and a trailing `/api` is stripped,
so all of these mean the same thing:

```
example.com                     https://example.com/mixpost
https://example.com/mixpost/    https://example.com/mixpost/api
```

## Configuration

Settings live in `~/.config/mixpost/config.yml` (0600), one entry per profile:

```yaml
current_profile: production
profiles:
  production:
    url: https://example.com/mixpost
    token: "..."
    workspace: 3bbd0951-5b04-432b-b2a0-688588b0720e
  staging:
    url: https://staging.example.com/mixpost
    token: "..."
```

Values resolve as **flag → environment variable → profile**:

| Variable | Flag | Meaning |
|---|---|---|
| `MIXPOST_URL` | `--url` | instance URL including the core path |
| `MIXPOST_TOKEN` | `--token` | access token |
| `MIXPOST_PROFILE` | `--profile` | profile to use |
| `MIXPOST_CONFIG` | none | path to the configuration file |
| `MIXPOST_OUTPUT` | `-o`, `--output` | default output format |
| `MIXPOST_INSECURE` | `--insecure` | skip TLS verification |

Switch instances per command with `--profile staging`, or for a whole session
with `mixpost config use staging`.

### The workspace is not an environment variable

There is no `MIXPOST_WORKSPACE`, deliberately. The URL and the token say **who
you are**; the workspace says **what you operate on**. One token reaches many
workspaces, so a resource selector carried in ambient environment state would
let the same command line mean different things in different shells:

```bash
mixpost post delete <uuid> --permanent --yes   # from which workspace, exactly?
```

A wrong URL or token fails loudly with 404 or 401. A wrong but valid workspace
succeeds, against the wrong data. So it comes from the profile, which you write
deliberately and can read back with `mixpost config list`, or from an explicit
`-w` on the command.

`-w` takes a uuid, a workspace name or a numeric id:

```bash
mixpost post list -w acme
mixpost post list -w 3bbd0951-5b04-432b-b2a0-688588b0720e
```

A name is resolved through `GET /workspaces` once per run; a uuid costs no extra
request.

## Output

`--output` accepts `table`, `json`, `yaml`, `csv`, `tsv`, `id` and `template`.
The default is **`table` on a terminal and `json` everywhere else**, so a
command in a pipe or a script always produces machine-readable output without
being told to.

```bash
mixpost post list                                  # table for a human
mixpost post list | jq '.data[].uuid'              # json in a pipe
mixpost post list -o id | xargs -n1 mixpost post get
mixpost post list -o csv --no-headers > posts.csv
mixpost post list -o template --template '{{range .data}}{{.uuid}} {{.status}}{{"\n"}}{{end}}'
```

JSON output is the API response passed through untouched, so fields this CLI
does not know about still reach you.

## Exit codes

```
0   success                     6   422 validation failed
1   generic failure             7   429 rate limited
2   bad invocation or config    8   5xx server error
3   401 not authenticated       9   network failure or timeout
4   403 forbidden              10   cancelled at a confirmation prompt
5   404 not found
```

When the output format is structured, failures are written to stderr as JSON so
an agent can read them the same way it reads data:

```json
{
  "error": {
    "message": "Validation errors",
    "status": 422,
    "exit_code": 6,
    "errors": { "versions.0.content": ["The content is required."] }
  }
}
```

## Commands

Workspace-scoped. These use the workspace from `-w` or the active profile:

```
mixpost account list|get
mixpost post    list|get|create|update|schedule|queue|approve|delete
mixpost tag     list|get|create|update|delete
mixpost media   list|get|upload|status|update|delete
```

Instance-level. No workspace needed:

```
mixpost workspace list    # the workspaces your token can reach
mixpost ping              # is the instance up and the token valid (Pro only)
```

Plus `auth`, `config`, `raw`, `version` and `completion`. Run
`mixpost <command> --help` for the flags of any of them.

### Scope

This CLI covers the API available in **Mixpost Pro and Enterprise**. The
Enterprise admin panel under `/api/panel`, covering workspace administration,
instance users, subscriptions and receipts, is deliberately not wrapped. Those
endpoints
are still reachable through the escape hatch if you run Enterprise and hold an
admin token:

```bash
mixpost raw GET panel/workspaces
```

### Posts

Repeating `--text` builds a thread; `--link` and `--media` line up with the text
blocks in the same order.

```bash
# draft
mixpost post create --text "Draft this for later" --account twitter

# scheduled, in an explicit timezone
mixpost post create \
  --text "Launching today" \
  --account twitter --account linkedin \
  --at "2026-09-10 15:00" --tz Europe/Chisinau

# publish now, with media and a tag that is created if missing
mixpost post create --text "Look at this" --media 4,5 \
  --account 1 --tag launch --create-tags --now

# a thread
mixpost post create --text "1/ First" --text "2/ Second" --account twitter --queue

# per-provider options
mixpost post create --text "Sensitive" --account mastodon \
  --option mastodon.sensitive=true \
  --option youtube.title="Release notes"
```

`--account` and `--tag` take an id, a uuid, a username, a display name or a
provider, and are resolved against the workspace before the post is sent.

Editing reads the post first and applies the flags on top of it, so changing the
text does not clear the accounts, tags or schedule:

```bash
mixpost post update <post-uuid> --text "Corrected text"
mixpost post update <post-uuid> --at "2026-09-11 09:00"
```

Deleting moves posts to the trash. `--permanent` deletes for good and asks first;
add `--yes` to confirm in a script.

```bash
mixpost post delete <post-uuid>
mixpost post delete <uuid-a> <uuid-b> --permanent --delete-mode app_and_social --yes
```

Full control is always available by handing over the JSON body, which the flags
then override:

```bash
cat post.json | mixpost post create -f -
mixpost post create -f template.json --at "2026-09-10 15:00"
```

### Media

```bash
mixpost media upload photo.jpg                      # single request
mixpost media upload video.mp4 --alt-text "Demo"    # chunked when it is large
mixpost media upload https://example.com/photo.jpg  # the instance downloads it
mixpost media upload *.png -o id                    # ids, ready to pipe
```

Files above `--chunk-threshold` (8 MiB by default) switch to the chunked
endpoints automatically: the CLI initiates a session, uploads the parts (three at
a time, `--concurrency`), completes it, and aborts the session on the server if
anything fails. Remote URLs are polled until the download finishes.

### Any endpoint

`raw` reaches endpoints the CLI does not wrap. The path is relative to `/api`
and is prefixed with the workspace, except for instance-level paths such as
`ping` and `workspaces`; `--no-workspace` forces that for any other path.

```bash
mixpost raw GET posts -q page=2
mixpost raw GET ping
mixpost raw POST tags -d '{"name":"launch","hex_color":"#60a5fa"}'
mixpost raw GET posts --paginate --limit 100
mixpost raw GET panel/workspaces        # Enterprise admin API
```

## Notes for automation and AI agents

- **stdout is data only.** Progress, prompts, warnings and errors go to stderr.
- **Formats are stable.** `-o json` returns the API response verbatim; `-o id`
  prints one identifier per line for `xargs`.
- **Exit codes are specific.** Branch on 3/4/5/6 instead of parsing messages.
- **`--dry-run` prints the request** (method, URL, headers and body) and
  performs no writes. Read-only lookups still run, because resolving `-w acme`
  or `--account twitter` to an id is what makes the printed request accurate.
- **Destructive commands ask first.** Without a terminal they refuse rather than
  assume, so an unattended run must pass `--yes` deliberately.
- **Writes are not retried blindly.** Retries cover rate limits, network faults
  and server errors on idempotent methods; a `POST` is only repeated after a
  `429`, which means the server did not process it. Tune with `--retries`.
- **Pagination is one flag.** `--all` walks every page, `--limit N` stops early.

A single request for an agent to see what is available:

```bash
mixpost --help && mixpost post --help
```

Mixpost also ships a built-in [MCP server](https://mixpost.app/mcp/). Use it for
agents that speak MCP; use this CLI for shell-driven agents, cron and CI, where
a binary is cheaper than a protocol.

## Development

```bash
make test      # unit and end-to-end tests against a mock API
make race      # the same under the race detector
make vet fmt
make build
make snapshot  # full release matrix locally, publishes nothing
```

Layout:

```
cmd/mixpost        entry point
internal/config    profiles on disk, URL normalisation
internal/mixpost   API client: requests, retries, pagination, uploads
internal/output    table, JSON, YAML, CSV, TSV, id and template rendering
internal/cli       cobra commands
```

Shell completion:

```bash
mixpost completion zsh  > "${fpath[1]}/_mixpost"
mixpost completion bash > /etc/bash_completion.d/mixpost
mixpost completion fish > ~/.config/fish/completions/mixpost.fish
```

## License

[MIT](LICENSE).
