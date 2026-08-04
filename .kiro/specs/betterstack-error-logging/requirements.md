# Requirements Document

## Introduction

The Fundis backend currently emits all logs as JSON to standard output using the Go standard library `log/slog` (35 `slog.Error`, 12 `slog.Warn`, and 6 `slog.Info` call sites across `auth`, `jobrequests`, `jobs`, `notifications`, `payments`, and `main.go`). Once a container restarts or scales, that output is the only record of a failure, and nobody is notified when something breaks.

This feature forwards error-severity and higher log records to Better Stack (formerly Logtail) so that failures are centrally searchable, alertable, and retained. Forwarding is implemented as an additional `slog.Handler` installed at startup via `slog.SetDefault`, so every existing call site is covered without editing 53 log statements.

Two properties shape the whole design:

1. **Low volume, high signal.** Only records at or above the configured severity threshold (ERROR by default) leave the process. Console output keeps every level exactly as today.
2. **Logging is never load-bearing.** A Better Stack outage, an expired token, or a slow network must never block an HTTP handler, crash the process, or cause a log-about-logging feedback loop.

Payloads are shaped for readability in the Better Stack UI: a stable set of top-level fields (`dt`, `level`, `message`, `service`, `environment`), a structured `error` object, source location, and all remaining structured attributes nested under `context` so they stay filterable without polluting the top-level namespace.

## Glossary

- **Fundis_Backend**: The Go/Echo API process defined by `main.go` in this repository.
- **Better_Stack**: The Better Stack Telemetry log management platform, which accepts logs over an HTTP ingest endpoint authenticated by a source token.
- **Source_Token**: The per-source credential issued by Better_Stack, sent as an HTTP `Authorization: Bearer` header value.
- **Ingest_Endpoint**: The HTTPS URL to which log batches are POSTed.
- **Log_Record**: A single `slog.Record` produced by any call site in the Fundis_Backend.
- **Log_Event**: The JSON object derived from a Log_Record that is transmitted to Better_Stack.
- **Log_Shipper**: The component that owns configuration, the Event_Queue, the delivery goroutine, and lifecycle (start, flush, close).
- **Better_Stack_Handler**: The `slog.Handler` implementation that filters Log_Records by severity, converts them into Log_Events, and enqueues them onto the Event_Queue.
- **Console_Handler**: The existing `slog.JSONHandler` writing to standard output, which remains the sole destination for records below the Shipping_Threshold.
- **Event_Queue**: The bounded in-memory buffer holding Log_Events awaiting delivery.
- **Ingest_Client**: The HTTP client that POSTs batches of Log_Events to the Ingest_Endpoint.
- **Redactor**: The component that replaces sensitive attribute values in a Log_Event before the Log_Event is enqueued.
- **Shipping_Threshold**: The minimum `slog.Level` at which a Log_Record is forwarded to Better_Stack.
- **Delivery_Batch**: One or more Log_Events transmitted in a single HTTP request as a JSON array.
- **Self_Diagnostic**: A message about the Log_Shipper's own operation, written only to the Console_Handler.

## Requirements


### Requirement 1: Configure Better Stack Forwarding Through Environment Variables

**User Story:** As a platform operator, I want Better Stack forwarding configured entirely through environment variables, so that I can enable centralized error logging per environment without changing or rebuilding application code.

#### Acceptance Criteria

1. WHEN the Fundis_Backend starts, THE Log_Shipper SHALL read `BETTERSTACK_SOURCE_TOKEN` exactly once and SHALL use that value, with leading and trailing whitespace removed, as the Source_Token.
2. WHEN the Fundis_Backend starts, THE Log_Shipper SHALL read `BETTERSTACK_INGEST_URL` exactly once and SHALL use that value, with leading and trailing whitespace removed, as the Ingest_Endpoint.
3. WHEN the Fundis_Backend starts, THE Log_Shipper SHALL read the service name from `BETTERSTACK_SERVICE_NAME` and SHALL use `fundis-backend` when that variable is unset, holds an empty value after whitespace removal, or holds more than 64 characters after whitespace removal.
4. IF `BETTERSTACK_SOURCE_TOKEN` is unset or holds an empty value after whitespace removal at startup, THEN THE Log_Shipper SHALL enter disabled state, write one Self_Diagnostic at WARN level naming `BETTERSTACK_SOURCE_TOKEN`, and transmit zero Log_Events for the lifetime of the process.
5. IF `BETTERSTACK_INGEST_URL` is unset or holds an empty value after whitespace removal at startup, THEN THE Log_Shipper SHALL enter disabled state and SHALL write one Self_Diagnostic at WARN level naming `BETTERSTACK_INGEST_URL`.
6. IF `BETTERSTACK_INGEST_URL` holds a value that fails URL parsing, specifies a scheme other than `https`, or omits a host, THEN THE Log_Shipper SHALL enter disabled state and SHALL write one Self_Diagnostic at ERROR level that names `BETTERSTACK_INGEST_URL` and excludes the Source_Token.
7. WHEN both `BETTERSTACK_SOURCE_TOKEN` and `BETTERSTACK_INGEST_URL` hold values that pass the validation stated in criteria 4 through 6, THE Log_Shipper SHALL enter enabled state before the Fundis_Backend accepts the first inbound HTTP request.
8. WHEN the Log_Shipper enters disabled state at startup, THE Log_Shipper SHALL remain in disabled state for the lifetime of the process, applying zero re-reads of any `BETTERSTACK_` environment variable after startup.
9. IF any `BETTERSTACK_` environment variable fails validation at startup, THEN THE Fundis_Backend SHALL continue startup until it accepts inbound HTTP requests and SHALL perform zero process exits attributable to Log_Shipper configuration.
10. WHILE the Log_Shipper is in disabled state, THE Fundis_Backend SHALL write every Log_Record of every level to standard output through the Console_Handler.
11. WHILE the Log_Shipper is in disabled state, THE Better_Stack_Handler SHALL add zero HTTP requests and zero background goroutines to the Fundis_Backend.
12. WHERE `APP_ENV` equals `production` under case-insensitive comparison after whitespace removal, WHILE the Log_Shipper is in disabled state, THE Log_Shipper SHALL write one Self_Diagnostic at ERROR level stating that centralized error logging is inactive.
13. THE Fundis_Backend SHALL declare `BETTERSTACK_SOURCE_TOKEN`, `BETTERSTACK_INGEST_URL`, `BETTERSTACK_SERVICE_NAME`, `BETTERSTACK_MIN_LEVEL`, `BETTERSTACK_BATCH_SIZE`, `BETTERSTACK_FLUSH_SECONDS`, and `BETTERSTACK_QUEUE_SIZE` in `.env.example` with empty or documented default values.
14. THE Log_Shipper SHALL obtain the Source_Token exclusively from the process environment, applying zero command-line flags, zero configuration files, and zero compiled-in default values.

### Requirement 2: Forward Only Error-Severity And Higher Records

**User Story:** As a platform operator, I want only error and critical records forwarded, so that the Better Stack source stays inside quota and every forwarded event is worth investigating.

#### Acceptance Criteria

1. THE Log_Shipper SHALL evaluate `BETTERSTACK_MIN_LEVEL` exactly once during startup by trimming leading and trailing whitespace from the value and folding the remaining characters to lower case, SHALL map the resulting value `warn` to `slog.LevelWarn` and the resulting value `error` to `slog.LevelError`, and SHALL hold the resulting Shipping_Threshold unchanged for the remainder of the process lifetime.
2. IF `BETTERSTACK_MIN_LEVEL` is unset or holds a value that becomes empty after whitespace trimming, THEN THE Log_Shipper SHALL set the Shipping_Threshold to `slog.LevelError` and SHALL write zero Self_Diagnostics about the Shipping_Threshold.
3. WHILE the Log_Shipper is in enabled state, WHEN a Log_Record with a level greater than or equal to the Shipping_Threshold is emitted, THE Better_Stack_Handler SHALL convert the Log_Record into a Log_Event and SHALL enqueue the Log_Event onto the Event_Queue.
4. WHILE the Log_Shipper is in enabled state, WHEN a Log_Record with a level below the Shipping_Threshold is emitted, THE Better_Stack_Handler SHALL return a nil error to the caller within 1 millisecond, SHALL add zero Log_Events to the Event_Queue, and SHALL produce zero serialized Log_Event bytes.
5. THE Console_Handler SHALL write every Log_Record of every level to standard output as one newline-terminated single-line JSON object whose bytes are identical to the bytes produced for the same Log_Record by the `slog.JSONHandler` configuration used by the Fundis_Backend before this feature, and SHALL apply zero redaction and zero truncation to Log_Records written to standard output.
6. WHEN the Echo recover middleware recovers a panic, THE Fundis_Backend SHALL emit exactly one Log_Record at ERROR level containing the panic value rendered as text truncated to its first 1024 characters and the captured stack trace truncated to its first 8192 bytes.
7. WHILE the Log_Shipper is in enabled state, WHEN a Log_Record carries a level greater than `slog.LevelError`, THE Better_Stack_Handler SHALL convert the Log_Record into a Log_Event and SHALL set the `level` field to the uppercase name reported by the level value.
8. IF `BETTERSTACK_MIN_LEVEL` holds a value that, after whitespace trimming and case folding, matches neither `warn` nor `error`, THEN THE Log_Shipper SHALL set the Shipping_Threshold to `slog.LevelError` and SHALL write exactly one Self_Diagnostic at WARN level containing the rejected value truncated to its first 64 characters.
9. WHEN the level-enabled check of the Better_Stack_Handler is invoked with a level below the Shipping_Threshold, THE Better_Stack_Handler SHALL return false.
10. WHILE the Log_Shipper is in enabled state, WHEN the level-enabled check of the Better_Stack_Handler is invoked with a level greater than or equal to the Shipping_Threshold, THE Better_Stack_Handler SHALL return true.

### Requirement 3: Emit Readable, Consistently Structured Log Events

**User Story:** As an on-call engineer, I want each forwarded event to carry a predictable set of well-named fields, so that the Better Stack UI renders errors readably and I can filter by service, environment, and error type.

#### Acceptance Criteria

1. THE Better_Stack_Handler SHALL emit each Log_Event as a single JSON object containing the top-level fields `dt`, `level`, `message`, `service`, `environment`, and `truncated`.
2. THE Better_Stack_Handler SHALL set `dt` to the Log_Record timestamp formatted as an RFC 3339 string in UTC with exactly three fractional-second digits.
3. THE Better_Stack_Handler SHALL set `level` to one of `DEBUG`, `INFO`, `WARN`, or `ERROR` when the Log_Record level equals a named `log/slog` level, and SHALL set `level` to the name of the nearest lower named level followed by `+` and the positive numeric offset when the Log_Record level does not equal a named level.
4. THE Better_Stack_Handler SHALL set `message` to the Log_Record message text, emitting an empty string when the Log_Record message text is empty.
5. THE Better_Stack_Handler SHALL set `service` to the first 64 characters of the configured service name, using `unknown` when the configured service name is unset or empty.
6. WHEN the Better_Stack_Handler handles a Log_Record carrying one or more attributes whose values satisfy the `error` interface, THE Better_Stack_Handler SHALL emit an `error` object built from the first such attribute in Log_Record attribute order, containing `message` set to the error text and `type` set to the concrete Go type name of that error value, and SHALL place every remaining error-valued attribute under the `context` object as its error text.
7. WHEN the Better_Stack_Handler emits an `error` object whose error chain contains at least one wrapped error, THE Better_Stack_Handler SHALL emit `error.chain` as an array of at most 10 error texts produced by successive unwrapping, ordered from outermost to innermost, omitting entries beyond the tenth and setting `truncated` to `true` when the chain contains more than 10 errors.
8. WHEN a Log_Record carries call-site information, THE Better_Stack_Handler SHALL emit a `source` object containing `file` set to the base file name, `line` set to the line number as a JSON number, and `function` set to the function name of the Log_Record call site.
9. THE Better_Stack_Handler SHALL emit all remaining Log_Record attributes under a `context` object, preserving `slog.Group` nesting as nested JSON objects to a maximum nesting depth of 8 levels, and SHALL omit attributes nested deeper than 8 levels while setting `truncated` to `true`.
10. IF a Log_Record attribute key matches one of the reserved top-level keys `dt`, `level`, `message`, `service`, `environment`, `error`, `source`, `context`, or `truncated`, THEN THE Better_Stack_Handler SHALL place that attribute under the `context` object and SHALL leave the reserved top-level field value unchanged.
11. WHEN two attributes sharing one key occur within the same JSON object level of the emitted Log_Event, THE Better_Stack_Handler SHALL retain the value of the attribute appearing last in the Log_Record and SHALL emit that key exactly once.
12. IF a serialized Log_Event exceeds 65536 bytes, THEN THE Better_Stack_Handler SHALL truncate string values under `context` in descending order of byte length and SHALL then truncate `message`, cutting each value at a UTF-8 character boundary and appending a truncation indicator, until the serialized Log_Event occupies at most 65536 bytes, and SHALL set `truncated` to `true`.
13. THE Better_Stack_Handler SHALL emit, for every Log_Record, a Log_Event that parses as valid UTF-8 encoded JSON, representing nil attribute values as JSON null, replacing each byte sequence that is not valid UTF-8 with the Unicode replacement character, and representing values whose type has no JSON representation as their default Go string formatting.
14. THE Better_Stack_Handler SHALL emit `truncated` set to `false` for every Log_Event that undergoes zero truncation.
15. THE Better_Stack_Handler SHALL set `environment` to the first 64 characters of the value of `APP_ENV`, using `unknown` when `APP_ENV` is unset or holds an empty value.
16. IF a Log_Record carries no call-site information, THEN THE Better_Stack_Handler SHALL omit the `source` object and SHALL emit all other required top-level fields of the Log_Event.
17. THE Better_Stack_Handler SHALL emit `dt`, `level`, `service`, `environment`, `truncated`, `error.type`, and every `source` field without truncation for every Log_Event.

### Requirement 4: Correlate Forwarded Events With Requests And Users

**User Story:** As an on-call engineer, I want forwarded errors to carry request and user identifiers, so that I can reconstruct a single failing request across multiple log lines.

#### Acceptance Criteria

1. WHEN Fundis_Backend receives an inbound HTTP request, THE Fundis_Backend SHALL assign exactly one request identifier to that request, consisting of 8 to 200 characters drawn only from ASCII alphanumeric characters, hyphen, and underscore, and distinct from the request identifier of every other request handled by the same running process instance.
2. WHEN Fundis_Backend sends an HTTP response, THE Fundis_Backend SHALL set the `X-Request-Id` response header of that response to the request identifier assigned to the corresponding request, including responses produced by centralized error handling and by recovered panics.
3. WHEN a client supplies an `X-Request-Id` request header whose value is non-empty, is at most 200 characters long, and contains only ASCII alphanumeric characters, hyphen, and underscore, THE Fundis_Backend SHALL adopt the supplied value byte-for-byte as the request identifier for that request.
4. IF a client supplies an `X-Request-Id` request header whose value is empty, exceeds 200 characters, or contains any character outside the set of ASCII alphanumeric characters, hyphen, and underscore, THEN THE Fundis_Backend SHALL discard the supplied value, SHALL assign a generated request identifier that satisfies criterion 1, and SHALL continue processing the request without rejecting it or returning an error response caused by the invalid header.
5. WHEN the Better_Stack_Handler converts a Log_Record that carries a request identifier propagated from the request context, THE Better_Stack_Handler SHALL emit in the resulting Log_Event a `request` object containing `id` set to that request identifier, `method` set to the HTTP method of the request, and `path` set to the matched route value defined by criteria 8 and 9.
6. IF an authenticated user identifier is present in the request context at the time a Log_Record is created, THEN THE Better_Stack_Handler SHALL emit `user.id` in the resulting Log_Event set byte-for-byte to that authenticated user identifier.
7. WHEN the Better_Stack_Handler converts a Log_Record that carries no request identifier, THE Better_Stack_Handler SHALL emit the Log_Event with the `request` object omitted and SHALL complete the conversion and enqueue the Log_Event to the Event_Queue without raising an error and without discarding the Log_Event.
8. THE Better_Stack_Handler SHALL populate `request.path` with the registered route template of the matched route, with no path parameter value substituted into the emitted value.
9. IF an inbound HTTP request matches no registered route, THEN THE Better_Stack_Handler SHALL emit `request.path` set to a single fixed placeholder value denoting an unmatched route, and SHALL still emit `request.id` and `request.method` for that Log_Event.
10. IF no authenticated user identifier is present in the request context at the time a Log_Record is created, THEN THE Better_Stack_Handler SHALL omit the `user` object from the resulting Log_Event.
11. WHILE Fundis_Backend is handling an inbound HTTP request, THE Fundis_Backend SHALL propagate that request's single assigned request identifier to every Log_Record emitted through the request-scoped logger derived from that request's context, including Log_Records emitted by functions invoked with that context, so that all such Log_Events carry an identical `request.id` value.

### Requirement 5: Deliver Log Events Without Blocking Request Handling

**User Story:** As a customer using the Fundis API, I want request latency to stay independent of log shipping, so that a slow or unreachable logging provider never delays my request.

#### Acceptance Criteria

1. WHEN the Better_Stack_Handler enqueues a Log_Event onto the Event_Queue, THE Better_Stack_Handler SHALL return control to the calling goroutine before the Ingest_Client opens the HTTP request that carries that Log_Event to the Ingest_Endpoint.
2. WHILE the Event_Queue holds free capacity, THE Better_Stack_Handler SHALL return from each enqueue operation within 1 millisecond measured on the calling goroutine from entry to return of the enqueue operation.
3. WHEN the Log_Shipper starts in enabled state, THE Log_Shipper SHALL start exactly one dedicated background goroutine and SHALL transmit every Delivery_Batch to the Ingest_Endpoint from that goroutine only.
4. THE Log_Shipper SHALL read the maximum Delivery_Batch size from `BETTERSTACK_BATCH_SIZE`, SHALL use 20 when that variable holds an empty value, a value that fails base-10 integer parsing, or an integer value outside the range 1 through 1000, and SHALL write one Self_Diagnostic at WARN level naming the rejected value.
5. WHILE the Log_Shipper is in enabled state, WHEN the count of Log_Events held in the Event_Queue reaches the maximum Delivery_Batch size, THE Log_Shipper SHALL transmit a Delivery_Batch containing exactly that number of Log_Events within 100 milliseconds of the count reaching that size, without waiting for the flush interval to elapse.
6. THE Log_Shipper SHALL read the flush interval in seconds from `BETTERSTACK_FLUSH_SECONDS`, SHALL use 5 when that variable holds an empty value, a value that fails base-10 integer parsing, or an integer value outside the range 1 through 300, and SHALL write one Self_Diagnostic at WARN level naming the rejected value.
7. WHILE the Event_Queue holds at least one Log_Event, WHEN the flush interval elapses measured from the later of the completion of the previous Delivery_Batch transmission and the previous flush interval boundary, THE Log_Shipper SHALL transmit a Delivery_Batch containing every queued Log_Event up to the maximum Delivery_Batch size, in enqueue order.
8. THE Ingest_Client SHALL transmit each Delivery_Batch as a single HTTP POST request to the Ingest_Endpoint with a body containing a JSON array of the Log_Events in that Delivery_Batch ordered by enqueue order.
9. THE Ingest_Client SHALL set the `Content-Type` header to `application/json` and the `Authorization` header to `Bearer ` followed by the Source_Token.
10. THE Ingest_Client SHALL apply a per-request timeout of 10 seconds covering connection, transmission, and response reading, and SHALL abort a request whose elapsed time exceeds that timeout and report a transport-level failure for that Delivery_Batch to the Log_Shipper.
11. THE Ingest_Client SHALL create exactly one `http.Client` instance for the lifetime of the process and SHALL transmit every Delivery_Batch through that instance with idle connection reuse enabled.
12. WHILE the Event_Queue holds zero Log_Events, WHEN the flush interval elapses, THE Log_Shipper SHALL transmit zero HTTP requests and SHALL restart the flush interval.
13. WHILE the Ingest_Client waits for a response from the Ingest_Endpoint, THE Better_Stack_Handler SHALL continue accepting Log_Events into free Event_Queue capacity and SHALL return from each enqueue operation within 1 millisecond, independent of the elapsed response time of the Ingest_Endpoint.
14. WHEN adding a queued Log_Event to a Delivery_Batch would raise the serialized request body above 1048576 bytes, THE Log_Shipper SHALL transmit the Delivery_Batch without that Log_Event and SHALL retain that Log_Event for the next Delivery_Batch.
15. WHILE a Delivery_Batch transmission is in progress, WHEN the flush interval elapses or the queued Log_Event count reaches the maximum Delivery_Batch size, THE Log_Shipper SHALL transmit zero additional HTTP requests until the in-progress transmission reaches a terminal state.

### Requirement 6: Bound Memory Use Under Log Bursts

**User Story:** As a platform operator, I want log buffering to consume a bounded amount of memory, so that an error storm degrades logging rather than exhausting process memory.

#### Acceptance Criteria

1. THE Log_Shipper SHALL read the Event_Queue capacity from `BETTERSTACK_QUEUE_SIZE` as a base-10 integer and SHALL use 1000 when that variable holds an empty value, a value that fails integer parsing, or an integer outside the range 1 through 100000.
2. THE Event_Queue SHALL hold at most the configured capacity in Log_Events at every instant, including while enqueue operations from multiple goroutines occur concurrently.
3. WHILE the Event_Queue holds Log_Events equal to the configured capacity, IF an enqueue operation occurs, THEN THE Better_Stack_Handler SHALL discard the incoming Log_Event, increment the dropped-event counter by 1, and return control to the calling goroutine within 1 millisecond.
4. WHEN the dropped-event counter increases AND either at least 60 seconds have elapsed since the most recent drop Self_Diagnostic or zero drop Self_Diagnostics have been written since process start, THE Log_Shipper SHALL write one Self_Diagnostic at WARN level containing the cumulative dropped-event count and the configured Event_Queue capacity.
5. WHEN the Better_Stack_Handler discards a Log_Event, THE Console_Handler SHALL write the corresponding Log_Record to standard output in the same JSON format applied to Log_Records that are enqueued successfully.
6. THE Log_Shipper SHALL retain at most the sum of the configured Event_Queue capacity and the configured maximum Delivery_Batch size, multiplied by 65536 bytes, of Log_Event payload data in memory at any instant, counting queued Log_Events together with the Delivery_Batch awaiting a response.
7. IF `BETTERSTACK_QUEUE_SIZE` holds a non-empty value that fails integer parsing or lies outside the range 1 through 100000, THEN THE Log_Shipper SHALL write one Self_Diagnostic at WARN level naming `BETTERSTACK_QUEUE_SIZE` and stating the applied capacity of 1000.
8. IF the dropped-event counter increases WHILE fewer than 60 seconds have elapsed since the most recent drop Self_Diagnostic, THEN THE Log_Shipper SHALL write zero Self_Diagnostics for that discard and SHALL include that discard in the cumulative dropped-event count reported by the next drop Self_Diagnostic.
9. WHEN the delivery goroutine removes Log_Events from a full Event_Queue such that the Event_Queue holds at least one free slot, THE Better_Stack_Handler SHALL enqueue each subsequent Log_Event and SHALL leave the dropped-event counter unchanged for those Log_Events.


### Requirement 7: Retry Transient Ingest Failures And Abandon Permanent Ones

**User Story:** As an on-call engineer, I want transient delivery failures retried and permanent failures reported once, so that brief network problems lose no errors while a bad token does not generate infinite retry noise.

#### Acceptance Criteria

1. WHEN the Ingest_Endpoint returns HTTP status 202, THE Log_Shipper SHALL discard the transmitted Delivery_Batch without retransmission and SHALL increment the delivered-event counter by the number of Log_Events in that Delivery_Batch.
2. IF the Ingest_Endpoint returns HTTP status 429 or any status in the range 500 through 599 AND the transmission-attempt count for that Delivery_Batch is less than 5, THEN THE Log_Shipper SHALL retransmit that Delivery_Batch containing the same Log_Events in the same order after waiting the retry delay, and SHALL increment the retried-batch counter by 1.
3. IF the Ingest_Client reports a transport-level failure or the 10-second per-request timeout elapses AND the transmission-attempt count for that Delivery_Batch is less than 5, THEN THE Log_Shipper SHALL retransmit that Delivery_Batch containing the same Log_Events in the same order after waiting the retry delay, and SHALL increment the retried-batch counter by 1.
4. THE Log_Shipper SHALL set the base retry delay for a Delivery_Batch to 1 second before the first retransmission, SHALL double the base retry delay before each subsequent retransmission up to a maximum of 30 seconds, and SHALL use the value of the `Retry-After` response header expressed in seconds, clamped to the range 1 through 300 seconds, in place of the computed base retry delay when a response carrying HTTP status 429 supplies that header with a value that parses as a non-negative integer.
5. THE Log_Shipper SHALL wait a retry delay equal to the base retry delay plus a jitter value drawn from a uniform distribution over the closed interval from 0 to 20 percent of the base retry delay, so that every retry delay lies in the range from the base retry delay to 1.2 times the base retry delay and never exceeds 360 seconds.
6. THE Log_Shipper SHALL maintain a transmission-attempt count per Delivery_Batch that equals 1 after the first transmission of that Delivery_Batch, increases by exactly 1 on each retransmission of that Delivery_Batch, and never exceeds 5, so that the Log_Events of one Delivery_Batch appear in at most 5 HTTP requests to the Ingest_Endpoint.
7. WHEN the transmission-attempt count for one Delivery_Batch reaches 5 AND that fifth transmission ends in a status or transport failure classified as retryable by criteria 2 and 3, THE Log_Shipper SHALL discard that Delivery_Batch, increment the dropped-event counter by the number of Log_Events in that Delivery_Batch, increment the abandoned-batch counter by 1, and write one Self_Diagnostic at ERROR level containing the number of discarded Log_Events and the last observed response status or transport failure category.
8. IF the Ingest_Endpoint returns HTTP status 403, THEN THE Log_Shipper SHALL discard the Delivery_Batch without retransmission, discard every Log_Event held in the Event_Queue, increment the dropped-event counter by the total number of Log_Events discarded, enter disabled state for the remaining lifetime of the process, and write one Self_Diagnostic at ERROR level stating that the Source_Token was rejected.
9. WHILE the Log_Shipper is in disabled state after HTTP status 403, THE Log_Shipper SHALL transmit zero HTTP requests to the Ingest_Endpoint, including at flush interval boundaries and during the close operation.
10. IF the Ingest_Endpoint returns HTTP status 402, THEN THE Log_Shipper SHALL discard the Delivery_Batch without retransmission, increment the dropped-event counter by the number of Log_Events in that Delivery_Batch, suspend transmission for 15 minutes during which it transmits zero HTTP requests, write one Self_Diagnostic at ERROR level stating that Better_Stack rejected the Delivery_Batch for account or quota reasons, and resume transmission at the first flush interval boundary after the 15 minutes elapse.
11. IF the Ingest_Endpoint returns HTTP status 406 or 413, THEN THE Log_Shipper SHALL discard the Delivery_Batch without retransmission, increment the dropped-event counter by the number of Log_Events in that Delivery_Batch, and write one Self_Diagnostic at ERROR level containing the response status and the number of discarded Log_Events.
12. WHILE the Log_Shipper waits for a retry delay or for a 15-minute suspension period, THE Better_Stack_Handler SHALL continue accepting Log_Events into the Event_Queue up to the configured Event_Queue capacity, and SHALL discard each Log_Event arriving while the Event_Queue holds Log_Events equal to that capacity and increment the dropped-event counter for each discarded Log_Event.
13. THE Log_Shipper SHALL exclude the Source_Token from every Self_Diagnostic describing an ingest failure.
14. WHEN the Ingest_Endpoint returns any status in the range 200 through 299 other than 202, THE Log_Shipper SHALL discard the transmitted Delivery_Batch without retransmission, increment the delivered-event counter by the number of Log_Events in that Delivery_Batch, and write one Self_Diagnostic at WARN level containing the response status.
15. IF the Ingest_Endpoint returns any status outside the range 200 through 299 that is not 402, 403, 406, 413, 429, and not within the range 500 through 599, THEN THE Log_Shipper SHALL discard the Delivery_Batch without retransmission, increment the dropped-event counter by the number of Log_Events in that Delivery_Batch, and write one Self_Diagnostic at ERROR level containing the response status.
16. THE Ingest_Client SHALL read at most 4096 bytes of each ingest response body, discard the bytes read, and close the response body before the Log_Shipper acts on the response status, so that the underlying connection returns to the connection pool for reuse on every status and on every attempt.

### Requirement 8: Isolate Logging Failures From Application Behavior

**User Story:** As a platform operator, I want the log shipper to fail silently and locally, so that a logging provider incident never turns into an application incident or a self-amplifying log loop.

#### Acceptance Criteria

1. THE Log_Shipper SHALL write every Self_Diagnostic through a logger bound directly to the Console_Handler, bypassing the Better_Stack_Handler, and SHALL tag every Self_Diagnostic with a dedicated marker attribute whose presence identifies the record as Log_Shipper self-diagnostic output.
2. THE Log_Shipper SHALL perform zero Event_Queue enqueue operations and zero Ingest_Endpoint transmissions for any Self_Diagnostic, including Self_Diagnostics at levels greater than or equal to the Shipping_Threshold, so that a Self_Diagnostic describing a delivery failure causes zero additional delivery attempts.
3. IF a panic occurs on the delivery goroutine, THEN THE Log_Shipper SHALL recover the panic, discard the in-flight Delivery_Batch, increment the dropped-event counter by the number of Log_Events in that Delivery_Batch, write one Self_Diagnostic at ERROR level containing the panic value truncated to at most 1024 characters and the stack trace truncated to at most 8192 characters, and resume delivery of subsequent Delivery_Batches within 1 second.
4. WHEN the delivery goroutine recovers its 5th panic within the process lifetime, THE Log_Shipper SHALL enter disabled state permanently for the remainder of the process lifetime, start zero replacement delivery goroutines, and write one Self_Diagnostic at ERROR level stating that log delivery is permanently stopped after repeated panics.
5. IF a panic occurs during conversion of a Log_Record into a Log_Event, THEN THE Better_Stack_Handler SHALL recover the panic, discard that partially converted Log_Event, increment the dropped-event counter by 1, leave the Event_Queue length and the contents of every previously enqueued Log_Event unchanged, and return control to the calling goroutine.
6. WHILE the Ingest_Endpoint remains unreachable, THE Fundis_Backend SHALL continue serving HTTP requests, returning for each request the same HTTP status code it returns for that request while the Log_Shipper is in disabled state, and SHALL continue writing every Log_Record of every level to standard output.
7. WHILE the Ingest_Endpoint remains unreachable, THE Fundis_Backend SHALL add at most 1 millisecond to the 99th-percentile latency of any HTTP request relative to the same request served while the Log_Shipper is in disabled state.
8. THE Better_Stack_Handler SHALL return a nil error from every `Handle` invocation and SHALL propagate zero panics into the calling goroutine for any Log_Record, including Log_Records carrying nil attribute values, invalid UTF-8 byte sequences, self-referential values, values of unsupported types, and values whose `String` or `LogValue` method panics.
9. WHEN the Log_Shipper enters disabled state during process operation, THE Better_Stack_Handler SHALL discard subsequent Log_Events performing zero enqueue operations and zero HTTP requests, and THE Console_Handler SHALL continue writing every Log_Record of every level to standard output.

### Requirement 9: Flush Pending Events On Graceful Shutdown

**User Story:** As an on-call engineer, I want buffered errors delivered before the process exits, so that the errors explaining a crash or a rolling deployment failure reach Better Stack.

#### Acceptance Criteria

1. WHEN the Fundis_Backend receives SIGTERM or SIGINT, THE Fundis_Backend SHALL initiate Echo graceful shutdown, SHALL invoke the Log_Shipper close operation only after the Echo shutdown call returns, and SHALL exit the process only after the close operation returns.
2. WHILE Echo graceful shutdown is in progress, THE Better_Stack_Handler SHALL convert every Log_Record at or above the Shipping_Threshold into a Log_Event and enqueue that Log_Event onto the Event_Queue, so that Log_Records emitted during Echo shutdown are eligible for transmission by the close operation.
3. WHEN the close operation is invoked, THE Log_Shipper SHALL transmit every Log_Event held in the Event_Queue at the moment of invocation, together with every Log_Event enqueued while the close operation is in progress, in Delivery_Batches of at most the configured maximum Delivery_Batch size.
4. WHILE the close operation is in progress, THE Better_Stack_Handler SHALL enqueue each Log_Event at or above the Shipping_Threshold onto the Event_Queue while the Event_Queue holds free capacity and SHALL return control to the calling goroutine within 1 millisecond.
5. THE Log_Shipper SHALL return from the close operation within 5 seconds of the first close invocation, and THE Log_Shipper SHALL treat this 5 second budget as a fixed value that no environment variable alters.
6. WHILE the close operation is in progress, THE Ingest_Client SHALL apply a per-request timeout equal to the lesser of the time remaining in the 5 second close budget and the 10 second per-request timeout defined in Requirement 5.
7. IF Log_Events remain untransmitted when the 5 second close budget expires, THEN THE Log_Shipper SHALL discard the remaining Log_Events, increment the dropped-event counter by the number of discarded Log_Events, write exactly one Self_Diagnostic at ERROR level containing the number of discarded Log_Events, and return from the close operation.
8. WHEN the close operation is invoked concurrently from more than one goroutine, THE Log_Shipper SHALL perform the flush exactly once, SHALL block every concurrent invocation until that flush reaches a terminal state, and SHALL then return the same outcome to every blocked invocation.
9. WHEN the close operation is invoked after a previous close operation has returned, THE Log_Shipper SHALL return control to the caller within 1 millisecond, transmitting zero HTTP requests and raising zero panics.
10. WHILE the Log_Shipper is in disabled state, WHEN the close operation is invoked, THE Log_Shipper SHALL return control to the caller within 10 milliseconds, transmitting zero HTTP requests and writing zero Self_Diagnostics.
11. WHEN an enqueue operation occurs after the close operation has returned, THE Better_Stack_Handler SHALL discard the Log_Event, increment the dropped-event counter, and return control to the calling goroutine within 1 millisecond, WHILE the Console_Handler continues writing the corresponding Log_Record to standard output.
12. WHILE the close operation is in progress, THE Log_Shipper SHALL apply a single transmission attempt per Delivery_Batch, performing zero backoff waits.

### Requirement 10: Keep Secrets And Personal Data Out Of Forwarded Payloads

**User Story:** As a security reviewer, I want forwarded log payloads free of credentials and personal data, so that shipping logs to a third-party provider introduces no new data exposure.

#### Acceptance Criteria

1. THE Redactor SHALL evaluate each key path segment of a Log_Event attribute independently, rather than the joined dotted path, against the case-insensitive substrings `token`, `secret`, `password`, `passwd`, `authorization`, `api_key`, `apikey`, `access_key`, `private_key`, `session`, `cookie`, `jwt`, and `bearer`, and SHALL replace the value of every attribute having at least one matching segment with the JSON string `[REDACTED]`.
2. THE Redactor SHALL apply key matching at every nesting depth within the `context`, `error`, `request`, and `user` objects of the Log_Event, and SHALL leave the top-level fields `dt`, `level`, `message`, `service`, `environment`, `source`, and `truncated` unmodified.
3. THE Redactor SHALL preserve the complete set of attribute keys present in the Log_Record and SHALL represent every redacted value as the JSON string `[REDACTED]` regardless of whether the original value was a string, number, boolean, null, object, or array, replacing a matched object or array value and every value nested beneath it with that single string.
4. THE Log_Shipper SHALL exclude the Source_Token from every Log_Event payload, every Self_Diagnostic, and every HTTP request body.
5. WHEN the Log_Shipper writes a Self_Diagnostic containing the Ingest_Endpoint, THE Log_Shipper SHALL emit the scheme, host, and path of the Ingest_Endpoint and SHALL exclude the query string and any userinfo component.
6. THE Fundis_Backend SHALL emit zero Log_Records whose message text or attribute values contain an end-user personal name, email address, or phone number, referring to persons by opaque identifier only, which is the invariant asserted by `notifications/activity_test.go`.
7. THE Better_Stack_Handler SHALL exclude HTTP request bodies, HTTP response bodies, and HTTP request headers from every Log_Event.
8. WHEN a Log_Record attribute value is a struct that defines a `LogValue` method, THE Better_Stack_Handler SHALL emit the value returned by that method, so that call sites keep control over the representation of their own types.
9. WHEN the Better_Stack_Handler converts a Log_Record into a Log_Event, THE Better_Stack_Handler SHALL apply the Redactor to the complete Log_Event after resolving `LogValue` methods and before serializing the Log_Event and before enqueueing the Log_Event onto the Event_Queue, so that the Event_Queue holds zero unredacted Log_Events.
10. IF a Log_Event contains a value nested more than 10 levels below the Log_Event root, THEN THE Redactor SHALL replace that value with the JSON string `[TRUNCATED]`, SHALL perform zero further descent beneath that value, and SHALL set `truncated` to `true`.
11. THE Redactor SHALL select values for redaction using attribute key matching and equality with the Source_Token only, replacing any attribute value equal to the Source_Token with the JSON string `[REDACTED]`, and SHALL perform zero inspection of attribute value content for secret-shaped patterns such as `Bearer ` prefixes or JWT-shaped strings.

### Requirement 11: Report The Log Shipper's Own Health

**User Story:** As a platform operator, I want visibility into whether log shipping is working, so that I can detect silent loss of error telemetry.

#### Acceptance Criteria

1. THE Log_Shipper SHALL maintain, as unsigned 64-bit values that never decrease for the lifetime of the process including after the close operation completes, counters for enqueued Log_Events, delivered Log_Events, dropped Log_Events, recovered conversion panics, retried Delivery_Batches, and abandoned Delivery_Batches.
2. IF a counter reaches the maximum value representable in an unsigned 64-bit integer, THEN THE Log_Shipper SHALL hold that counter at the maximum value, SHALL continue delivering Log_Events, and SHALL complete every subsequent increment without panicking.
3. THE Log_Shipper SHALL expose the counter values through a method returning a snapshot struct that additionally reports the current Event_Queue depth and the current count of in-flight Log_Events, where an in-flight Log_Event is a Log_Event removed from the Event_Queue whose Delivery_Batch has not yet reached a terminal state.
4. THE Log_Shipper SHALL populate every returned snapshot from a single consistent observation, such that every returned snapshot satisfies the equality of the enqueued count with the sum of the delivered count, the dropped count, the in-flight count, and the Event_Queue depth.
5. THE Log_Shipper SHALL maintain one dropped-event counter for each of the drop reasons Event_Queue at capacity, abandoned Delivery_Batch, non-retryable ingest response, recovered conversion panic, and enqueue after close, and SHALL keep the sum of those per-reason counters equal to the total dropped-event counter in every returned snapshot.
6. WHEN 5 minutes elapse since the previous periodic summary evaluation AND at least one counter increased since that evaluation, THE Log_Shipper SHALL write, within 5 seconds of that interval boundary, exactly one Self_Diagnostic at INFO level through a logger bound directly to the Console_Handler, containing every counter value, every per-reason dropped-event count, the Event_Queue depth, and the in-flight count from one snapshot.
7. WHEN 5 minutes elapse since the previous periodic summary evaluation AND zero counters increased since that evaluation, THE Log_Shipper SHALL write zero Self_Diagnostics and SHALL transmit zero HTTP requests to the Ingest_Endpoint.
8. THE Log_Shipper SHALL derive zero Log_Events from any periodic summary Self_Diagnostic or startup Self_Diagnostic, so that no summary is forwarded to Better_Stack.
9. WHEN the Log_Shipper starts in enabled state, THE Log_Shipper SHALL write exactly one Self_Diagnostic at INFO level through a logger bound directly to the Console_Handler containing the Shipping_Threshold name, the maximum Delivery_Batch size, the flush interval in seconds, the Event_Queue capacity, and the Ingest_Endpoint host, and SHALL exclude the Source_Token and the Ingest_Endpoint query string from that Self_Diagnostic.
10. WHILE the Log_Shipper is in disabled state, THE Log_Shipper SHALL return a snapshot from every invocation of the snapshot method without panicking, SHALL leave every counter value unchanged, and SHALL write zero periodic summary Self_Diagnostics.
11. WHEN the Log_Shipper enters disabled state at startup, THE Log_Shipper SHALL report the value zero for every counter, for the Event_Queue depth, and for the in-flight count in every returned snapshot.
12. THE Log_Shipper SHALL support concurrent counter increments and snapshot reads from multiple goroutines such that a test exercising concurrent enqueue, delivery, and snapshot operations under the Go race detector reports zero data races and every returned snapshot satisfies the accounting equality in criterion 4.

### Requirement 12: Verify Behavior Without Network Access

**User Story:** As a developer, I want the log shipper covered by tests that need no network and no Better Stack account, so that `make test` stays fast, deterministic, and runnable in CI.

#### Acceptance Criteria

1. THE Log_Shipper SHALL accept, at construction time, an explicit configuration value carrying the Ingest_Endpoint URL of 1 to 2048 characters, the Source_Token of 0 to 256 characters, the Shipping_Threshold, the flush interval from 1 millisecond to 300 seconds, and the maximum Delivery_Batch size from 1 to 1000 Log_Events, and SHALL read no process environment variable during construction or during delivery, so that tests direct traffic to an `httptest.Server` without mutating process environment state.
2. THE Log_Shipper SHALL derive every flush interval, retry backoff delay, and backoff jitter value solely from a clock abstraction and a jitter source supplied at construction time, such that advancing the supplied clock rather than elapsed wall-clock time triggers flushes and retries, no Log_Shipper test performs a real sleep longer than 50 milliseconds, and the Log_Shipper tests add no more than 5 seconds of wall-clock time to `make test`.
3. IF the configuration supplied at construction contains an empty Source_Token, as occurs when `BETTERSTACK_SOURCE_TOKEN` is unset in the test environment, THEN THE Log_Shipper SHALL enter disabled state, SHALL route every Log_Record to the Console_Handler only, and SHALL open zero outbound network connections for the lifetime of the process.
4. THE Log_Shipper SHALL expose a synchronous flush operation that accepts a caller-supplied deadline between 1 millisecond and 60 seconds, returns only after every queued Log_Event has reached a terminal state of delivered or dropped or the deadline has elapsed, and reports the count of delivered Log_Events, the count of dropped Log_Events, and whether the Event_Queue drained fully, so that tests assert on delivered payloads without polling.
5. WHEN `make test` executes `go test ./... -v`, THE Fundis_Backend SHALL report zero failures and zero skipped tests across the pre-existing test suite, with zero edits to pre-existing test assertions.
6. THE Fundis_Backend SHALL implement Better_Stack delivery using only the Go standard library packages `net/http` and `encoding/json`, adding zero third-party module requirements to `go.mod`.
7. THE Log_Shipper SHALL operate as an independent instance whose Event_Queue, Ingest_Client, Redactor, clock, jitter source, and Self_Diagnostic writer are scoped to that instance, and SHALL rely on no package-level mutable state, so that at least 8 Log_Shipper instances constructed and exercised concurrently by parallel tests produce independent, non-interfering delivered and dropped counts.
8. THE Self_Diagnostic SHALL write its output to a writer supplied at construction time, defaulting to standard error when no writer is supplied, so that tests capture diagnostic output in memory without reading process output streams.
9. THE Better_Stack_Handler SHALL be constructible without a Log_Shipper and without starting any background delivery goroutine, so that Log_Record-to-Log_Event conversion and Redactor behavior are verifiable in isolation with zero outbound network connections.


## Correctness Properties For Property-Based Testing

These properties are the machine-checkable core of the requirements above. Each one holds for arbitrary generated inputs and is a candidate for a property-based test with at least 100 iterations. All of them exercise Fundis code with an in-process HTTP test server, so the cost per iteration stays low.

**P1 — Severity filter is a faithful filter (model-based, Requirement 2 criteria 3, 4, 9, and 10).**
For an arbitrary sequence of Log_Records, the multiset of delivered Log_Events equals the multiset produced by applying `level >= Shipping_Threshold` to that sequence. The reference model is a one-line `filter` over the input slice.

**P2 — Conversion round-trips through JSON (round-trip, Requirement 3 criteria 1 through 5, 13, and 15).**
For an arbitrary Log_Record, decoding the serialized Log_Event yields `message`, `level`, `dt`, and every non-redacted attribute value equal to the corresponding Log_Record field. This is the parser/serializer round-trip property, and it is the highest-value test in this feature.

**P3 — Every Log_Record produces valid JSON (error conditions, Requirement 3 criterion 13 and Requirement 8 criterion 8).**
For arbitrary attribute values, including nil, invalid UTF-8 byte sequences, deeply nested groups, channel and function values, and values whose `String` method panics, the produced Log_Event parses as valid JSON and never panics the caller.

**P4 — Redaction is idempotent (idempotence, Requirement 10 criteria 1 through 3).**
For an arbitrary Log_Event, `redact(redact(event))` equals `redact(event)`.

**P5 — Redaction preserves structure (invariant, Requirement 10 criteria 1 through 3).**
For an arbitrary Log_Event, redaction leaves the set of keys and the nesting shape unchanged, and changes only values whose keys match the sensitive-substring set.

**P6 — Batching boundaries do not change the delivered multiset (confluence, Requirement 5 criteria 5, 7, and 8, and Requirement 7 criterion 1).**
For an arbitrary sequence of Log_Events and an arbitrary Delivery_Batch size in the range 1 through 100, the multiset of Log_Events received by the test server is identical, given a server that always returns 202.

**P7 — Queue capacity is never exceeded (invariant, Requirement 6 criteria 2 and 3).**
For an arbitrary burst of enqueue operations, queue length never exceeds the configured capacity at any observation point, and every discarded Log_Event increments the dropped-event counter exactly once. The full accounting equality is covered by P13.

**P8 — Size cap is respected (invariant, Requirement 3 criteria 12, 14, and 17).**
For arbitrary Log_Records, including records with multi-megabyte messages, every serialized Log_Event occupies at most 65536 bytes, every Log_Event exceeding that bound before truncation carries `truncated: true`, and `dt`, `level`, `service`, `environment`, `error.type`, and every `source` field survive truncation intact.

**P9 — Enqueue never blocks (metamorphic, Requirement 5 criteria 2 and 13).**
For an arbitrary sequence of enqueue operations against a test server that never responds, total enqueue duration stays below a bound proportional to the number of operations, demonstrating independence from server latency.

**P10 — Close is idempotent (idempotence, Requirement 9 criteria 8 and 9).**
For an arbitrary number of concurrent close invocations, the operation returns without panic, and the count of HTTP requests observed matches the count observed for a single close invocation.

**P11 — Non-retryable statuses terminate delivery (model-based, Requirement 7 criteria 1 through 3, 6, 10, 11, 14, and 15).**
For an arbitrary status code, the transmission-attempt count equals 1 for status 202, for every other status in the range 200 through 299, for statuses 402, 403, 406, and 413, and for every status outside the range 200 through 299 that is neither 429 nor within the range 500 through 599, and lies in the range 2 through 5 for status 429 and statuses in the range 500 through 599.

**P12 — Secrets never appear on the wire (invariant, Requirement 10 criteria 1 through 4 and 11).**
For an arbitrary Source_Token and arbitrary Log_Records carrying attributes with sensitive keys, no request body received by the test server contains the Source_Token or any generated sensitive value.

**P13 — Snapshot accounting is exact (invariant, Requirement 11 criteria 4, 5, and 12).**
For an arbitrary interleaving of enqueue, delivery, drop, and snapshot operations across multiple goroutines, every returned snapshot satisfies `enqueued == delivered + dropped + in_flight + queue_depth`, and the sum of the five per-reason dropped-event counters equals the total dropped-event counter.

**P14 — Redaction depth cap terminates (invariant, Requirement 10 criterion 10).**
For arbitrary Log_Events, including events whose attribute graphs nest hundreds of levels deep or contain a cycle, redaction returns without panic and without unbounded recursion, replaces every value nested more than 10 levels below the Log_Event root with `[TRUNCATED]`, and sets `truncated` to `true` for exactly those events that contain such a value.

**P15 — Delivery_Batch bodies stay under the 1 MiB cap (invariant, Requirement 5 criterion 14).**
For an arbitrary sequence of Log_Events of arbitrary sizes and an arbitrary maximum Delivery_Batch size, every request body received by the test server occupies at most 1048576 bytes, every body contains at least one Log_Event, and the multiset of Log_Events received across all bodies equals the multiset enqueued.

**P16 — Retry delays stay within the jitter bounds (invariant, Requirement 7 criteria 4 and 5).**
For an arbitrary sequence of retryable responses, including HTTP status 429 responses carrying arbitrary `Retry-After` header values, every observed retry delay lies in the closed interval from the base retry delay to 1.2 times the base retry delay and never exceeds 360 seconds, where the base retry delay follows the doubling schedule capped at 30 seconds or the clamped `Retry-After` value.

### Behavior Better Suited To Example-Based Tests

The following behaviors vary little with input and are covered by one to three representative integration or unit tests rather than property-based tests: the `Authorization` header format, the disabled-state startup path, the `X-Request-Id` response header, the startup Self_Diagnostic contents, and the periodic counter summary.
