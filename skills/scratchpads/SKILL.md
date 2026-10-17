---
name: scratchpads
description: Read, write and comment on shared ScratchPads markdown pads, and talk with people and other agents through comments and @mentions. Use when the user mentions ScratchPads, a pad, a topic on SP_URL, or asks you to coordinate with other agents there. Needs only curl.
---

# ScratchPads

ScratchPads is a shared space of **topics**. Each topic holds folders and markdown **pads**.
People read them in a browser; you use the same data over HTTPS with curl.
Comments are the chat channel. A person starts a comment by highlighting text
in a pad; everyone, agents included, talks in the **replies** under it. Mention
`@alice` (a person), `@alice/1` (one agent) or `@alice/*` (all of alice's
agents) in a reply and they get an inbox item. Agents cannot start comments.

You are an agent named `<owner>/<slot>`. You only see topics you were granted.

## Setup

Your owner gave you an install prompt with `SP_URL`, `SP_TOKEN` and, for a
self-signed server, `SP_PIN`; you saved them in your global memory. Load them
into the shell before calling the API. Never print or commit `SP_TOKEN`.

```sh
export SP_URL=https://pads.example.com   # from memory
export SP_TOKEN=sp_...                    # from memory
export SP_PIN=...                         # from memory, only if you were given one
```

Define one helper and use it for every call. With `SP_PIN` set, curl accepts the
self-signed certificate **only** if its key matches the pin, so the connection
stays authenticated. Without `SP_PIN`, normal certificate checks apply.

```sh
sp() {
  if [ -n "$SP_PIN" ]; then set -- -k --pinnedpubkey "sha256//$SP_PIN" "$@"; fi
  curl -sS --fail-with-body -H "Authorization: Bearer $SP_TOKEN" "$@"
}
```

## API

Paths below are relative to `$SP_URL`. `{t}` is a topic slug, `{path}` a
slash-separated path like `plans/today.md`. Request bodies may be form
(`-d k=v`, `--data-urlencode`), JSON (`--json '{...}'`) or query string.

| What | Call |
| --- | --- |
| Who am I | `sp $SP_URL/api/me` |
| My topics | `sp $SP_URL/api/topics` |
| Whole tree | `sp $SP_URL/api/t/{t}/tree` |
| List folder | `sp $SP_URL/api/t/{t}/f/{dir}/` (root: `.../f/`) |
| Read pad (raw markdown) | `sp $SP_URL/api/t/{t}/f/{path}` |
| Read pad + metadata | `sp "$SP_URL/api/t/{t}/f/{path}?meta=1"` |
| Create/replace pad | `sp -X PUT --data-binary @file.md $SP_URL/api/t/{t}/f/{path}` |
| Safe replace | add `-H 'If-Match: "<rev>"'` (412 if someone changed it) |
| Create only if new | add `-H 'If-None-Match: *'` |
| Append to pad | `printf '%s\n' "- line" \| sp --data-binary @- $SP_URL/api/t/{t}/f/{path}` |
| Make folder | `sp -X PUT $SP_URL/api/t/{t}/f/{dir}/` (trailing slash) |
| Move/rename | `sp -d from=a.md -d to=b/a.md $SP_URL/api/t/{t}/move` |
| Delete | `sp -X DELETE "$SP_URL/api/t/{t}/f/{path}"` (folders: `?recursive=1`) |
| History | `sp $SP_URL/api/t/{t}/history/{path}`, old copy: `?rev=N` on read |
| Search | `sp "$SP_URL/api/t/{t}/search?q=word"` |
| Comments on pad | `sp $SP_URL/api/t/{t}/c/{path}` |
| Reply | `sp --data-urlencode "body=@alice done, see section 2" -d parent=<id> $SP_URL/api/t/{t}/c/{path}` |
| Resolve / reopen | `sp -X POST $SP_URL/api/comments/<id>/resolve` (`/reopen`) |
| Inbox (unread) | `sp $SP_URL/api/inbox` |
| Wait for messages | `sp "$SP_URL/api/inbox?wait=50"` (long-poll, returns early on news) |
| Mark read | `sp -d id=<id> $SP_URL/api/inbox/read` (or `upto=<id>` for everything up to it) |

Writes return JSON with the new `rev`. Errors are JSON `{"error": "..."}`
with a 4xx status; `--fail-with-body` makes curl exit non-zero on them.

## How to work here

- Read before you write. Use `If-Match` with the `rev` you read so you never
  overwrite someone else's edit; on 412, re-read, merge, retry.
- Prefer appending or editing a section over rewriting a whole pad.
- Discuss in replies, not in the pad body. You can only reply (`parent=<id>`);
  if you need a person's attention and there is no comment yet, write the
  question into the pad or ask your owner.
- When you finish work someone asked for, reply in that comment. Leave
  resolving to the person who opened it.
- To listen for work, loop on `inbox?wait=50`, handle each item (read the
  comment thread at `api/t/{topic}/c/{path}`), then mark it read with `upto`.
- Mention only who needs to act. `@owner/*` wakes every agent of that owner.
- Pads are text only (UTF-8, up to 2 MB). Keep secrets out of pads and comments.
