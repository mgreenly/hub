# SSE Outbox Messaging Protocol

This document describes a small messaging protocol built from an atomic outbox,
Server-Sent Events, durable consumer cursors, and TCP backpressure. It is written
in the abstract: producer, consumer, event, cursor, and feed are protocol roles,
not application-specific concepts.

The protocol is intended for systems that want durable local event feeds without
operating a separate broker. It favors low idle cost, simple recovery, ordinary
HTTP tooling, and explicit backup/restore semantics over cluster-wide high
availability or broker-managed routing.

## Model

A producer owns a durable append-only outbox table in its local database. Domain
changes and their corresponding event rows are committed in the same local
transaction. Consumers subscribe to the producer's feed over a long-lived
HTTP `GET` that returns a Server-Sent Events stream.

The producer never calls consumers. Consumers read from producers. A consumer's
position is represented by an opaque cursor that the producer mints and the
consumer stores durably. On reconnect, the consumer presents its last committed
cursor and the producer resumes strictly after that position.

This creates a simple division of responsibility:

- The producer records facts durably and serves them in order.
- The consumer stores its own recovery position.
- The transport streams bytes and provides backpressure.
- The protocol does not require a broker, queue daemon, or central consumer
  registry.

## Requirements

A conforming implementation must preserve these properties:

- Event publication is atomic with the domain write that caused it.
- The producer's feed is ordered, and no event can become visible behind a
  cursor already issued for a later event.
- Consumers never parse, compare, or construct cursors.
- Consumers reconnect using the last durably committed cursor, not the last
  received cursor.
- A quiet, caught-up feed does not poll or spin.
- Slow consumers apply backpressure rather than causing unbounded buffering.
- Backup, restore, rebuild, or log truncation must be detected when they make a
  stored cursor invalid.

## Producer Outbox

Publishing an event is a single insert into the producer's outbox table inside
the same transaction as the domain change:

```sql
CREATE TABLE outbox (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id   TEXT    NOT NULL,
  type       TEXT    NOT NULL,
  payload    TEXT    NOT NULL,
  created_at TEXT    NOT NULL
);
```

The schema above is a reference SQLite shape. Other databases may use additional
columns, but the same logical fields are required:

- `seq` or equivalent producer position, used by the producer to order the feed.
- `event_id`, a stable unique identity for the event.
- `type`, a message type used for dispatch and filtering.
- `payload`, the event-specific JSON body.
- `created_at`, the event emission time.

The outbox row is the durable message. Any in-process notification mechanism is
only a doorbell: it wakes parked feed loops after commit and carries no event
data. Missing a doorbell signal must not lose an event.

## Ordering

The producer must serve events in a stable order where position assignment,
commit order, and visibility order are compatible. A consumer asks for "events
strictly after this cursor"; that query is safe only if an event can never commit
later with a position behind the cursor already returned to the consumer.

Single-writer engines such as SQLite can satisfy this with a simple increasing
sequence. Concurrent-writer engines need a dialect-specific stable frontier. For
example, a database where sequence assignment and commit order can differ must
avoid serving rows from transactions that may still be in flight, and may need a
compound cursor.

This is why cursors are opaque. The producer is free to encode whatever ordering
state its storage engine requires.

## Connection

A consumer connects with an HTTP request:

```http
GET /feed HTTP/1.1
Accept: text/event-stream
X-Consumer-Id: <stable-consumer-id>
Last-Event-ID: <opaque-committed-cursor>
```

`X-Consumer-Id` is a stable identifier for observability and future retention
models. `Last-Event-ID` is optional. When present, it contains only an opaque
cursor previously received in an event frame and durably committed by the
consumer.

When no cursor exists, the first subscription chooses one of two start modes:

- From the beginning: omit `Last-Event-ID`.
- From the current tail: omit `Last-Event-ID` and add `?from=tail`.

The tail choice is only a first-subscription bootstrap choice. After a consumer
has committed any cursor, reconnects must use that committed cursor.

The response is an SSE stream:

```http
HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
```

## Event Frames

A real event frame contains exactly one cursor, one event type, and one JSON
envelope:

```text
id: <opaque-cursor>
event: <message-type>
data: {"id":"<event-id>","type":"<message-type>","source":"<producer>","time":"<rfc3339>","payload":{...}}

```

The three ids are different:

| Location      | Meaning  | Purpose                             | Opaque to Consumer |
| ------------- | -------- | ----------------------------------- | ------------------ |
| SSE `id:`     | cursor   | stream position and resume          | yes                |
| envelope `id` | event id | identity and deduplication          | no                 |
| `payload.id`  | domain id | entity identity inside the payload | no                 |

The envelope fields are:

- `id`: stable event identity, generated once and preserved on replay.
- `type`: message type, mirrored from the SSE `event:` line.
- `source`: producer identity.
- `time`: event emission time.
- `payload`: event-specific JSON object.

The `data:` body must be compact single-line UTF-8 JSON. Event frames must carry
all three fields: `id:`, `event:`, and `data:`.

## Control Frames

Control and liveness frames must not carry `id:`. A frame without `id:` cannot
advance a consumer cursor.

Defined control frames:

```text
: keepalive

```

An idle liveness comment. It is not dispatched as an event and carries no data.

```text
event: caught-up
data: {}

```

The producer has sent everything currently available through its head.

```text
event: status
data: {"behind": 42}

```

Optional telemetry. The consumer may log it but must not use it to interpret or
compare cursors.

```text
event: resync
data: {"reason":"stale-epoch"}

```

The producer cannot honor the presented cursor. It must flush this frame and
close the connection. A consumer that fully receives `resync` must treat it as
authoritative, clear its stored cursor, and reconnect fresh.

Defined resync reasons:

- `stale-epoch`: cursor belongs to an older producer lineage.
- `diverged`: cursor is ahead of the producer's current head.
- `past-horizon`: cursor is below the retained event horizon; loss was detected
  but cannot be recovered from the feed.
- `unintelligible-cursor`: cursor cannot be parsed or belongs to a different
  feed.

## Cursor Rules

A cursor is a producer-minted opaque string. The consumer may only:

- receive it from an event frame's `id:` line;
- persist it durably;
- present it as `Last-Event-ID` on reconnect.

The consumer must not parse, compare, increment, decrement, or synthesize
cursors. The cursor column in consumer state should be text, even when a current
producer implementation happens to encode a number inside it.

For deployments where producer storage can be restored or rebuilt, cursors must
include a producer generation or epoch token. The token lets the producer reject
cursors minted before a restore, preventing sequence reuse from being mistaken
for continuity.

## Consumer State

A reference consumer offset table:

```sql
CREATE TABLE feed_offset (
  source     TEXT    PRIMARY KEY,
  cursor     TEXT,
  subscribed INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT    NOT NULL
);
```

`source` keys the upstream feed. `cursor` stores the committed opaque cursor.
`subscribed` records that the first-subscription choice has been made. This
prevents a consumer that chose `tail`, received no events, and restarted before
any cursor commit from repeatedly reusing the initial tail choice and silently
dropping events that arrived while it was down.

For consumers with durable controlled-side effects, a deduplication table is
also required:

```sql
CREATE TABLE dedup (
  event_id TEXT PRIMARY KEY,
  source   TEXT NOT NULL,
  acted_at TEXT NOT NULL
);
```

The event id, effect, dedup insert, and cursor advance must be coordinated in
one local transaction. If the effect fails, the consumer must not commit the
cursor; it retries the same event after reconnect or retry. If the event is a
duplicate, the consumer runs no effect but still commits the cursor advance.

Consumers whose effect is explicitly best-effort may omit deduplication and
advance the cursor after attempting the effect once.

## Processing Rules

A consumer processes one upstream feed in order:

1. Load committed cursor state.
2. Connect with `Last-Event-ID` if a cursor exists.
3. For each event frame, parse the envelope.
4. If the event type is relevant, apply the effect.
5. Commit the new cursor according to the effect semantics.
6. Reconnect on transport failure using the last committed cursor.

Filtering is consumer-side by default. Producers stream all event types. A
consumer may ignore event types it does not care about, but it must still commit
the cursor for skipped events. Otherwise the skipped event returns on every
reconnect.

Transport failures are not protocol conclusions. A dropped connection, non-200
response, incomplete frame, failed dial, or stalled stream is handled by
reconnecting with the committed cursor. A fully received `resync` is different:
it is a producer conclusion that the stored position is invalid.

## Backpressure

The protocol relies on TCP flow control. A consumer receives the next event by
reading bytes. If it stops reading, the receive window fills and the producer's
write blocks. The producer must therefore avoid breaking this chain:

- Fetch events from storage in bounded batches.
- Do not load all events after the cursor into memory.
- Do not place an unbounded queue between storage and the socket.
- Flush event frames promptly.

With these rules, backlog remains in the producer's durable outbox rather than
moving into producer or consumer memory.

## Retention

The producer may retain events by a time or size horizon:

```text
delete events at or below the retention floor
```

The horizon must exceed the maximum expected consumer downtime. If a consumer
reconnects with a cursor below the retained floor, the producer emits
`resync` with reason `past-horizon`. This reports real loss; it cannot recover
events already removed.

A stronger retain-until-committed model can be added later. It requires producer
knowledge of consumer committed positions, leases or TTLs for dead consumers,
and an acknowledgement path or forced reconnect cadence because SSE is
one-way.

## Delivery Semantics

The protocol provides at-least-once delivery from producer outbox commit through
consumer cursor commit, subject to retention. Duplicates are possible whenever a
consumer applies an effect but crashes before committing the cursor. Consumers
that need exactly-once effects must implement idempotency with the envelope
event id.

The protocol does not define guarantees for systems outside the consumer's local
transaction. External calls may be made durable with a local pending table and
deduplication, or treated as best-effort. That choice is effect-specific and not
part of the feed protocol.

## Non-Goals

This protocol does not specify:

- cross-host service discovery;
- authentication or authorization;
- consumer groups or partition rebalancing;
- producer-side event filtering;
- global event type registries;
- binary payload encoding;
- exactly-once delivery without consumer idempotency;
- retention based on committed offsets.

Broker clustering, leader election, and multi-box operation are stronger than
non-goals: they are outside the intended scope of this protocol. The protocol is
for a single box, where each producer's local database is the durable log and
loopback or otherwise local HTTP is the transport.

Some features above can be added without changing that scope, such as
producer-side filtering or stronger retention based on committed offsets. The
core design remains intentionally small: durable local outbox, long-lived SSE
feed, opaque committed cursors, and backpressure through the socket.
