# Event topic namespace policy

Canonical topic shape for every event the foo binaries publish to
`kit/bus`. One policy across all three binaries so sibling tools
(aps, ctxt, tlc) can subscribe predictably.

## Shape

```
[source].[category].[object].[action]
```

Four lowercase, dot-separated segments. `action` is past-tense (the
event already happened). This is the house event-bus notation; it
refines the 3-part `<tool>.<noun>.<verb>` form in the CLI conventions
doc (§9) by inserting an explicit `category` segment that maps to the
binary's help-group taxonomy (§4).

| Segment | Meaning | Source of truth |
|---|---|---|
| `source` | publishing binary | `foo`, `foo-scrape`, `foo-youtube` |
| `category` | domain group | the command's help GroupID (§4): `knowledge`, `organize`, `capture`, … |
| `object` | the noun acted on | `pattern`, `schema`, `fragment`, `model`, `embedding`, `page`, `transcript` |
| `action` | past-tense verb | `created`, `updated`, `deleted`, `compiled`, `scraped`, `fetched` |

## Registry

### foo (host)

| Mutation | Topic |
|---|---|
| `pattern create` | `foo.knowledge.pattern.created` |
| `pattern delete` | `foo.knowledge.pattern.deleted` |
| `pattern import` | `foo.knowledge.pattern.imported` |
| `fragment create` | `foo.knowledge.fragment.created` |
| `fragment delete` | `foo.knowledge.fragment.deleted` |
| `schema create` | `foo.knowledge.schema.created` |
| `schema delete` | `foo.knowledge.schema.deleted` |
| `schema compile` | `foo.knowledge.schema.compiled` |
| `embed add` / `embed file` | `foo.knowledge.embedding.created` |
| `embed collection delete` | `foo.knowledge.collection.deleted` |
| `model default` | `foo.organize.model.selected` |
| `provider` mutations | `foo.organize.provider.<action>` |

### foo-scrape (sidecar)

| Mutation | Topic |
|---|---|
| URL scraped | `foo-scrape.capture.page.scraped` |

### foo-youtube (sidecar)

| Mutation | Topic |
|---|---|
| transcript extracted | `foo-youtube.capture.transcript.fetched` |
| metadata extracted | `foo-youtube.capture.metadata.fetched` |

## Rules

- `category` is taken from the command's existing GroupID — do not
  invent a new taxonomy for events. Sidecars whose sole job is
  capture use `capture`.
- `action` is always past-tense. A read (`list`, `show`, `search`)
  publishes nothing — only state mutations emit.
- The network adapter (`bus.NewNetworkAdapter`) MUST be wired for any
  of these to reach an external subscriber; a bare in-process
  `bus.New()` publishes to nobody. (See the host's `initializeRuntime`.)
- Payload carries the object's id + the actor; never the full body of
  a secret-bearing object.
