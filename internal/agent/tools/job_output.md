Read current stdout/stderr from a background shell by its shell_id.

- wait=false (the default) returns available output immediately.
- wait=true waits for completion for at most 10 seconds by default.
- timeout_seconds can set the wait to 1-30 seconds; 0 uses the default.
- When the wait expires, return current output with Status: running and an
  explicit timeout notice. The background job keeps running and remains tracked.
- Caller cancellation interrupts the wait without terminating the job.
- Repeated reads continue from the previous read position. Set
  from_start=true to reread retained output, or offset to an absolute byte
  position. If old output was dropped by the buffer limit, the result says so.
- Completion facts are reported independently: exit_code, signal, timed_out,
  aborted, and kill_reason.

For long-running dev servers, use wait=false to inspect startup logs and check
HTTP readiness separately. A server does not need to exit to be ready. A wait
timeout does not mean the server failed: do not start a duplicate server or
restart it just because this read timed out. Avoid repeatedly polling unchanged
logs; make a readiness check or continue other work.
