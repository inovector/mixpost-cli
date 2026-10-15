<p align="center">
  <img src="art/cover.png" alt="Mixpost CLI" width="900">
</p>

# About Mixpost CLI

Your whole social media workflow, one command away.

Mixpost CLI brings [Mixpost](https://mixpost.app) to the place you already work.
Draft, schedule and review your posts, and read how they performed, without
opening a browser tab, on Mixpost Cloud or your own self-hosted Pro or
Enterprise instance.

It also works beautifully with AI assistants like Claude, ChatGPT or Cursor. Ask
for three posts about your new feature scheduled for next week, and your agent
can do it end to end.

## Install

**macOS and Linux: Homebrew**

```bash
brew install inovector/tap/mixpost
```

**macOS and Linux: install script**

```bash
curl -fsSL https://raw.githubusercontent.com/inovector/mixpost-cli/main/install.sh | sh
```

The script picks the build for your system, verifies its checksum and installs
it to `/usr/local/bin`, or to `~/.local/bin` when that is not writable. Set
`MIXPOST_VERSION` to install a given release and `MIXPOST_INSTALL_DIR` to choose
the directory.

**Windows: Scoop**

```powershell
scoop bucket add inovector https://github.com/inovector/scoop-bucket
scoop install mixpost
```

**Binary release**

Download the archive for your platform from the
[releases page](https://github.com/inovector/mixpost-cli/releases) and put
`mixpost` on your `PATH`. On macOS, prefer Homebrew or the install script: a
binary downloaded through a browser is unsigned and Gatekeeper blocks it.

**Go**

```bash
go install github.com/inovector/mixpost-cli/cmd/mixpost@latest
```

**Docker**

```bash
docker run --rm -e MIXPOST_PERSONAL_KEY \
  ghcr.io/inovector/mixpost-cli:latest post list -w acme
```

**From source**

```bash
git clone https://github.com/inovector/mixpost-cli && cd mixpost-cli
make build && ./mixpost version
```

## Quick start

Create a personal key: open the user menu of your Mixpost dashboard, click
**API**, then **Create a personal key**. The key is shown once, so copy it
straight away. Then:

```bash
mixpost auth login --personal-key <personal-key>
mixpost workspace list                       # find your workspace UUID
mixpost config set workspace <workspace-uuid>

mixpost account list
mixpost post create --text "Hello from the CLI" --account twitter --now
```

Without a URL, the CLI talks to Mixpost Cloud (`https://cloud.mixpost.app/p`).
For a self-hosted instance, pass `--url` with the address you open Mixpost at,
including its core path:

```bash
mixpost auth login --url https://example.com/mixpost --personal-key <personal-key>
```

A trailing `/api` is stripped, so all of these mean the same thing:

```
example.com/mixpost             https://example.com/mixpost
https://example.com/mixpost/    https://example.com/mixpost/api
```

`cloud.mixpost.app` becomes `https://cloud.mixpost.app/p`. Any other address
without a path is tried with the default `/mixpost` core path first and at the
site root second, so an instance with an empty `MIXPOST_CORE_PATH` is found at
login too.

## Configuration

Settings live in `~/.config/mixpost/config.yml` (0600), one entry per profile:

```yaml
current_profile: production
profiles:
  production:
    url: https://example.com/mixpost
    personal_key: "..."
    workspace: 3bbd0951-5b04-432b-b2a0-688588b0720e
  staging:
    url: https://staging.example.com/mixpost
    personal_key: "..."
```

Values resolve as **flag → environment variable → profile**:

| Variable | Flag | Meaning |
|---|---|---|
| `MIXPOST_URL` | `--url` | self-hosted Mixpost address, including the core path (default: Mixpost Cloud) |
| `MIXPOST_PERSONAL_KEY` | `--personal-key` | personal key |
| `MIXPOST_PROFILE` | `--profile` | profile to use |
| `MIXPOST_CONFIG` | none | path to the configuration file |
| `MIXPOST_OUTPUT` | `-o`, `--output` | default output format |
| `MIXPOST_INSECURE` | `--insecure` | skip TLS verification |

Switch instances per command with `--profile staging`, or for a whole session
with `mixpost config use staging`.

### The workspace is not an environment variable

There is no `MIXPOST_WORKSPACE`, deliberately. The URL and the personal access
key say **who you are**; the workspace says **what you operate on**. One key reaches many
workspaces, so a resource selector carried in ambient environment state would
let the same command line mean different things in different shells:

```bash
mixpost post delete <uuid> --permanent --yes   # from which workspace, exactly?
```

A wrong URL or key fails loudly with 404 or 401. A wrong but valid workspace
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
mixpost account      list|get
mixpost post         list|get|create|update|schedule|queue|approve|delete
mixpost tag          list|get|create|update|delete
mixpost media        list|get|upload|status|update|delete
mixpost media folder list|create|update|delete
mixpost mention      list|get|create|update|delete
mixpost analytics    summary|account|post|posting-times|best-times
```

Instance-level. No workspace needed:

```
mixpost workspace list    # the workspaces your personal key can reach
mixpost ping              # is Mixpost up and the key valid (self-hosted Pro only)
```

Plus `auth`, `config`, `raw`, `version` and `completion`. Run
`mixpost <command> --help` for the flags of any of them.

### Scope

This CLI covers the API available in **Mixpost Cloud, Pro and Enterprise**. The
Enterprise admin panel under `/api/panel`, covering workspace administration,
instance users, subscriptions and receipts, is deliberately not wrapped. Those
endpoints are still reachable through the escape hatch if you run Enterprise
and use the personal key of an admin:

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

A time without an offset is read in `--tz` when given, and otherwise in the
timezone set in your Mixpost user settings. An RFC 3339 time with an offset
names an exact instant and is converted for you.

Editing reads the post first and applies the flags on top of it, so changing the
text does not clear the accounts, tags or schedule, and the post stays at the
same moment:

```bash
mixpost post update <post-uuid> --text "Corrected text"
mixpost post update <post-uuid> --at "2026-09-11 09:00"
```

Scheduling and approving can move a post as they go, and give an account a
time of its own; accounts without one publish with the post:

```bash
mixpost post schedule <post-uuid> --at "2026-09-10 15:00" --tz Europe/Chisinau \
  --account-at linkedin="2026-09-10 18:00"
mixpost post approve <post-uuid> --at "2026-09-11 09:00"
```

Before a post is scheduled, queued or approved, it is checked against what each
of its platforms requires. A post that falls short fails with exit code 6 and
one entry per unmet requirement, keyed `requirements.<scope>.<provider>.<rule>`.

Filters on `post list` run on the server, so they reach the whole workspace:

```bash
mixpost post list --status scheduled --account linkedin --all
mixpost post list --keyword launch --tag campaign
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

Folders organise the library without moving any file. A folder is named by
uuid, by name when the name is unique, or by its path from the root:

```bash
mixpost media folder create Q4 --parent Brand
mixpost media upload banner.png --folder Brand/Q4
mixpost media list --folder Brand/Q4 --type image --sort name
mixpost media update <media-uuid> --folder root --alt-text "Team photo"
mixpost media list --usage unused -o id | xargs mixpost media delete --yes
```

### Mentions

A mention holds the handle a person or brand goes by on each network. A post
mentions them with `{{mention:<reference>}}`, and every account receives the
identity its own network expects:

```bash
mixpost mention create --name "Ana Popescu" \
  --identity bluesky=ana.bsky.social --provider-id bluesky=did:plc:abcdef123456 \
  --identity twitter=anapopescu
mixpost mention list                      # the INSERT column is the text to paste
mixpost post create --text "Thanks {{mention:42}}!" --account twitter --account bluesky
mixpost mention update <mention-id> --remove-identity twitter
```

### Analytics

```bash
mixpost analytics summary --period last_month
mixpost analytics account linkedin --type content --sort-by IMPRESSION_COUNT
mixpost analytics post <post-uuid>
mixpost analytics posting-times --tz Europe/Chisinau
mixpost analytics best-times --tz Europe/Chisinau      # ready for post create --at
```

The table is a digest; `-o json` returns the full response, charts included.

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
