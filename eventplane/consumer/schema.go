package consumer

// SchemaSQL is the canonical feed_offset-table DDL (event-protocol.md §10.3,
// minus the dedup table). As with the producer's outbox.SchemaSQL, the library
// OWNS this DDL so every consumer's offset store is byte-identical; a consumer
// applies it through its own migration runner (single migration authority per DB
// file) and is expected to assert its migration matches this constant.
//
// One row per upstream feed, keyed on the upstream's envelope "source" (§8.3,
// §9.1). The cursor is stored as opaque TEXT (§9.1) and is NULLable because the
// entire recovery state is the committed cursor (§10): on a `tail` first
// subscription no cursor exists until the first commit. `subscribed` is the
// durable first-subscription marker (§7.1, §10): it is set to 1 before the first
// connect so a consumer that chose `tail`, processed nothing, then restarted
// before any cursor commit does not silently re-bootstrap as `tail` and re-drop
// the events that arrived while it was down.
//
// There is deliberately NO dedup table: this consumer's only effect is a
// best-effort external hop (event-protocol.md §11.2), which tolerates both loss
// and duplicates, so it needs no dedup record — the cursor is its only durable
// state.
const SchemaSQL = `CREATE TABLE feed_offset (
  source     TEXT    PRIMARY KEY,
  cursor     TEXT,
  subscribed INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT    NOT NULL
);
`
