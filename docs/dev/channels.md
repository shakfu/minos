# Typed channels

An exploration of channels that carry typed data, and of views that render it. Nothing here is built. The channel as it stands is [chat-concepts.md](chat-concepts.md) 2.4 and sections 4-5; its wire shape is [wire-contract.md](../wire-contract.md) 6 and 9-11.

Today every channel message is a `subject` and a text `body`. The proposal: a message also carries a **type**, such as `stock-quote`, and a front end chooses a **view** for that type, such as a table or a chart.

**Decided: the sequence contract stays universal.** Every message of every type is stored, takes a `seq`, and is repaired by wire-contract 8 without exception. A type never changes how a client judges a gap. Data that cannot live under that rule is a stream, a separate object deferred in section 6.

## 1. Three terms

- **Encoding** -- how the payload is serialised: UTF-8 text, Markdown, JSON. It says how to parse, not what the payload means.

- **Type** -- a named, versioned schema over an encoding, plus the rules the server applies to it. `stock-quote@1` is JSON with fields `symbol`, `price`, `currency`, `at`.

- **View** -- a rendering of messages of one or more types. Views belong to front ends. The server never knows which view anyone uses.

The user's example separates the first two: the packet is JSON, and the data is of type `stock-quote`. A registered media type can carry both with a structured suffix, as in `application/vnd.minos.stock-quote+json` ([RFC 6838 4.2.8](https://www.rfc-editor.org/rfc/rfc6838#section-4.2.8)). A short name with a version, `stock-quote@1`, is enough inside one server. Recommendation: the short name on the wire, with the encoding defined by the type.

## 2. What a type decides

The existing channel rules assume a message is prose. Each assumption becomes a property of the type. This table is the core of the proposal.

| Property | Question | Today, for every channel |
|-|-|-|
| Schema | Which payloads are valid? | Any non-empty text. |
| Subject | What is the one-line headline? | The first line of the body (wire-contract 10). |
| Opened | Does a subscriber open items one at a time? | Yes, except on `system`. |
| Key | Does a newer item supersede an older one with the same key, for reading? | No. |
| Search text | What does `archive.search` match? | Subject and body. |

Two properties are not the type's, because the contract is universal: every item is delivered, and every item is stored. 2.4 and 2.5 give the reasons.

### 2.1 Subject

The first-line rule gives `{` for a JSON body. Each type needs a subject derivation. For `stock-quote@1` it is `<symbol> <price> <currency>`. The server applies it, as it applies the first-line rule now, so every stored message still has a subject and a front end with no view for the type still shows a list.

### 2.2 Opened

The pending stack (chat-concepts 2.4) suits documents. It does not suit samples. A quote published once a second adds 86400 items a day to every subscriber's stack. The model already met this once: "`system` has no pending stack", because file events would pile up.

That exception generalises. **Whether items are opened is a property of the type, not of the channel `system`.** `event` is a type with opening off, and `system` is a channel of that type.

### 2.3 Key

A table of current prices wants the latest quote per symbol, not the last 200 quotes. `history` returns the tail, capped at 200, and is not pageable (wire-contract 8). With 500 symbols, the tail does not contain every symbol.

A type with a key allows a second read: the latest item per key. This is log compaction as Kafka defines it ([Kafka docs, Log Compaction](https://kafka.apache.org/documentation/#compaction)). It is a read, not a deletion. `seq` stays contiguous and archival stays as it is.

### 2.4 Delivery

Not a property of the type. Every item is delivered.

A live connection never skips a frame. Its outbound queue is FIFO with one writer, and a full queue closes the connection rather than dropping a frame (`go/internal/socket/socket.go:121`). The queue holds 256 frames, so at 100 items a second a client that stalls for 2.6 seconds loses its connection. The client reconnects and backfills what it missed through `history`.

A **latest** policy, replacing a queued item with a newer one for the same key, was considered and rejected. It would be the first mechanism to skip a frame on a live connection. The client would see the gap, request `history`, and receive the items coalescing skipped, which is a repair request per coalesced item. Avoiding that means telling the client not to repair, which makes wire-contract 8 conditional.

For a fast feed, two things stay within the contract:

- The publisher batches or throttles, so the rate a type admits fits a client's queue.

- A view discards superseded values on the client, after delivery.

### 2.5 Stored

Not a property of the type. Every item is stored, because a `seq` is assigned at storage (chat-concepts 2.3, *Transience*) and repair reads back from the store.

The cost falls on fast types. Every message is an `INSERT` in its own `BEGIN IMMEDIATE` transaction. The timeline holds each item until archival moves it to `archived_messages`, which never expires (chat-concepts 4, open question 1). A fast channel therefore needs a short archival period. Data too fast or too large to store item by item is a stream (section 6).

### 2.6 Search text

`archive.search` matches substrings of subject and body. On a JSON body, `price` matches every quote. A type lists the fields whose values are searched.

## 3. Types, from mail to numbers

Ordered from document-like to signal-like. Each row fixes the properties in section 2.

| Type | Encoding | Payload | Subject | Opened | Key | Views |
|-|-|-|-|-|-|-|
| `text@1` | text | the body | first line | yes | -- | list, reader |
| `event@1` | text | one line | the line | no | -- | log |
| `mail@1` | JSON | `from`, `subject`, `body` (Markdown), `refs` (VFS paths) | `subject` | yes | -- | inbox, reader |
| `alert@1` | JSON | `severity`, `source`, `summary`, `detail` | `<severity> <summary>` | yes | `source` | list by severity, reader |
| `record@1` | JSON | `level`, `source`, `message`, `fields` | `message` | no | -- | filtered table |
| `stock-quote@1` | JSON | `symbol`, `price`, `currency`, `at` | `<symbol> <price> <currency>` | no | `symbol` | quote table, line chart, ticker |
| `ohlc@1` | JSON | `symbol`, `open`, `high`, `low`, `close`, `volume`, `start`, `interval` | `<symbol> <close>` | no | `symbol` | candlestick chart, table |
| `metric@1` | JSON | `name`, `value`, `unit`, `at`, `labels` | `<name> <value><unit>` | no | `name` + `labels` | gauge, sparkline, table |

Notes on rows that strain the model:

- **`mail@1`.** Attachments as first-class objects are excluded (chat-concepts 6). `refs` names VFS paths instead, which the model already permits. `in-reply-to` is omitted because threading is also excluded. A mail channel is one-to-many. A private message to one person is a room, not a channel: "A channel wanting one reader has misidentified itself" (chat-concepts 2.4).

- **`alert@1`.** A keyed, opened type. A newer alert from the same source supersedes the older one in the latest-per-key read, but both remain in the log, and each must still be opened. Whether opening the newer one also opens the older is open question 2.

- **`stock-quote@1`.** Quote tables use the latest-per-key read. A chart of a day needs more than 200 points, which `history` does not give. See 4.3.

- **`metric@1`.** A channel type only at a rate a client's queue and the timeline absorb: a reading per minute, not per millisecond. Faster is a stream (section 6).

## 4. Wire changes, sketched

Additive, in the style of wire-contract 9-11.

### 4.1 Fields added to core shapes

- A channel carries `types`: the type names it accepts, fixed at `channel.create`. A channel with no `types` accepts `text@1`, which is every channel today.

- A message carries `type`, and `data` when the type's encoding is JSON. `body` stays a string. For a JSON type it is the server's text rendering of `data`, so a client with no view still shows something.

- `system` declares `types: ["event@1"]`.

`data` as a JSON value, over `body` holding JSON text: double encoding every quote costs bytes and forces every client to parse twice. Stored as `TEXT` in both cases, in a new `data` column beside `body`.

### 4.2 Rules

- `channel.publish` and `channel.submit` carry `type` and `data`. A type the channel does not accept is refused. A payload that does not match the type's schema is refused with the failing field named.

- The server derives `subject` and `body` from `data`. The publisher does not send them for a JSON type, so they cannot disagree with `data`.

- A channel's `types` may gain a version and may not lose one. A client holding `stock-quote@1` items must still find the type accepted after `@2` is added.

- Frames are bounded. A body is at most 64 KiB (wire-contract 6). A type states a maximum payload that fits under it.

### 4.3 Operations

| Op | Request fields | Reply |
|-|-|-|
| `channel.latest` | `channel` | `{channel, messages}`, the newest per key |
| `channel.range` | `channel`, `from`, `to`, `key`, `after` | `{channel, messages, more}`, ascending, pageable |

`channel.range` is a pageable read of live messages by time. It is the second pageable read after `archive.read`, and it has `history`'s authority check rather than an admin's. It does not replace `history`, whose tail cap serves gap repair (chat-concepts 4, *How the archive is read*).

Aggregation, such as one point per minute, is deliberately absent. A view can downsample what `channel.range` returns. Add it on evidence of transfer cost.

### 4.4 Where types are defined

| Option | For | Against |
|-|-|-|
| **Built into the server**, one Go file per type. | Subject, key, opening and search are behaviour; code expresses them. Stdlib `encoding/json` validates. | A new type is a server release. |
| **Registered by an admin** at runtime, with JSON Schema. | New types without a release. | Needs a schema library; none is in `go.mod`. Subject derivation needs a template language. |
| **Open**: any string, unvalidated. | No work. | A client parses untrusted, unchecked JSON. Search and keys cannot work. |

Recommendation: built in. Six to nine types cover section 3. Revisit when a real type is blocked on a release.

## 5. Where views live

- **A view is a front end's.** The terminal client can draw a table and a Unicode block sparkline; it cannot draw a candlestick. A browser can. So the view registry is per front end: type name to the views it can draw.

- **Every type has a fallback view: subject and body.** This is why the server derives `body` (4.1). It keeps `system`, the terminal client and any front end without a registry working.

- **A view has an arity.**

  - An *item* view renders one message: the mail reader.

  - A *snapshot* view renders the latest per key: the quote table. It reads `channel.latest`.

  - A *series* view renders a range: the chart. It reads `channel.range`, then applies pushes.

- **The chosen view is not stored on the server.** The model puts a fact on the server when "it is the same fact from every device" (chat-concepts 2.5). A view choice is not: the same person wants a chart in a browser and a table in a terminal. It stays client-local.

- **A payload is untrusted input to the view.** A publisher controls every field. A view never interprets a field as markup or script. This is the reason `readfile` serves most types as attachments (wire-contract 4, *Disposition*), and it applies to a `mail@1` body rendered as Markdown.

## 6. Streams, deferred

Because the sequence contract is universal, data that cannot be stored and delivered item by item is not a channel. The model's test for a concept is identity, lifetime and authority (chat-concepts 1). By that test:

- A **channel** is a sequence of stored items, each addressable by `seq`, repaired on loss, opened or archived.

- A **stream** is a current value that changes. Its items are not addressable, a lost value is superseded rather than repaired, and history, if kept, is a downsampled series rather than a log.

Candidates: a high-rate `metric`, and `samples` (`name`, `rate`, `start`, `values` as an array of numbers), viewed as a waveform or spectrum. A stream would reuse the channel audience rule and have no `seq`.

Its cost is a second object with its own subscription and delivery code, and a second thing for every front end to list. That is the precedent chat-concepts 5 set for submissions: an object outside the sequence rather than an exception to it.

Build channel types first: `mail@1`, `alert@1`, `stock-quote@1`. Design a stream when a real feed outruns a client's queue or the timeline's write rate.

## 7. Open questions

1. Are a channel's `types` fixed at creation, like a room's retention, or may an admin add one later? 4.2 allows adding a version only.

2. For a keyed, opened type, does opening the newest item for a key open the older ones?

3. Should rooms be typed? This proposal leaves every room `text@1`, because a room is a conversation (chat-concepts 2.4, *Rooms keep the log*). An agent posting structured results to a room ([design.md](design.md) 8.5) is the case that would reopen it, and that design needs exactly this: a payload the server passes through unread.

4. Does a moderator see a submission through the type's view, or as raw `data`? Moderating a rendered chart hides what the payload actually contains.
