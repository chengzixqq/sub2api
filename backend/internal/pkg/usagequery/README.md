# Usage Query Contract

Usage list, statistics, chart and error endpoints accept a paired `start_time`
and `end_time` in RFC3339 format with an offset. The range is `[start, end)`.
Supplying either new parameter selects this contract: both must be valid and
start must precede end. Valid timestamp pairs override legacy date parameters.
Legacy `start_date` and `end_date` retain inclusive natural-day semantics in
`timezone`, including daylight-saving transitions.

Successful query responses expose `data.query`:

```json
{
  "start_time": "2026-09-08T20:59:00+08:00",
  "end_time": "2026-09-08T21:00:00+08:00",
  "timezone": "Asia/Shanghai",
  "generated_at": "2026-09-08T13:00:01Z"
}
```

Only legacy unbounded lists have null range bounds. Chart buckets remain hour
or day buckets, formatted in the requested timezone.

Usage lists opt into `count_mode=deferred`. Their data contains `items`, `page`,
`page_size`, `has_more`, `total_exact: false`, `total: null`, and `pages: null`.
The matching statistics response supplies `total_requests` for exact pagination.
Exact mode remains available for exports and older clients; the explicit legacy
`exact_total=false` mode retains its existing lower-bound shape.

`force_refresh=true` (or legacy `nocache=true`) replaces the matching statistics
cache generation and uses raw trend data. Older in-flight work cannot overwrite
the refreshed entry. Snapshots load uncached components so TTLs do not stack.
Statistics caches retain at most 1,024 results for 30 seconds, with at most 128
distinct in-flight queries and a 30-second shared-work deadline. Cancelling one
waiter does not cancel others; cancelling the last waiter cancels database work.
Keys include identity, workspace/account scope, timezone, complete filters and
chart grouping. Failed queries are never cached.

Unfiltered trends reuse only complete hourly aggregates whose watermark and
calculation timestamps are fresh within five minutes. Raw queries cover partial
edges, missing buckets, stale buckets and unfinalized intervals in the same SQL
snapshot. Nonmatching timezones and scoped or filtered queries use raw data.
