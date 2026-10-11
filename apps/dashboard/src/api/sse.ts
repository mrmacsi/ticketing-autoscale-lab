// Server-Sent Events over fetch + ReadableStream so the Authorization header can be sent
// (EventSource cannot set headers). Parser follows the WHATWG "event stream interpretation" rules.

export interface SseMessage {
  event: string;
  data: string;
  id: string | null;
}

export class SseParser {
  private buffer = '';
  private dataLines: string[] = [];
  private eventName = '';
  private lastEventId: string | null = null;
  private sawCarriageReturn = false;
  /** Value of the last `retry:` field, if any. */
  retryMs: number | null = null;

  /** Feed a decoded text chunk; returns the messages completed by this chunk. */
  feed(chunk: string): SseMessage[] {
    const out: SseMessage[] = [];
    let text = chunk;
    // A CRLF split across chunks: drop the LF that belongs to a CR already processed.
    if (this.sawCarriageReturn && text.startsWith('\n')) text = text.slice(1);
    this.sawCarriageReturn = false;
    this.buffer += text;

    let start = 0;
    for (let i = 0; i < this.buffer.length; i++) {
      const ch = this.buffer[i];
      if (ch !== '\n' && ch !== '\r') continue;
      const line = this.buffer.slice(start, i);
      if (ch === '\r') {
        if (i + 1 < this.buffer.length) {
          if (this.buffer[i + 1] === '\n') i++;
        } else {
          this.sawCarriageReturn = true;
        }
      }
      start = i + 1;
      const msg = this.processLine(line);
      if (msg) out.push(msg);
    }
    this.buffer = this.buffer.slice(start);
    return out;
  }

  /** Reset per-connection state (a partial message is discarded on disconnect). */
  reset(): void {
    this.buffer = '';
    this.dataLines = [];
    this.eventName = '';
    this.sawCarriageReturn = false;
  }

  private processLine(line: string): SseMessage | null {
    if (line === '') return this.dispatch();
    if (line.startsWith(':')) return null; // comment, e.g. ": ping"
    const colon = line.indexOf(':');
    let field = line;
    let value = '';
    if (colon !== -1) {
      field = line.slice(0, colon);
      value = line.slice(colon + 1);
      if (value.startsWith(' ')) value = value.slice(1);
    }
    switch (field) {
      case 'event':
        this.eventName = value;
        break;
      case 'data':
        this.dataLines.push(value);
        break;
      case 'id':
        if (!value.includes('\0')) this.lastEventId = value;
        break;
      case 'retry':
        if (/^\d+$/.test(value)) this.retryMs = Number(value);
        break;
      default:
        break; // unknown fields are ignored
    }
    return null;
  }

  private dispatch(): SseMessage | null {
    if (this.dataLines.length === 0) {
      this.eventName = '';
      return null;
    }
    const msg: SseMessage = {
      event: this.eventName || 'message',
      data: this.dataLines.join('\n'),
      id: this.lastEventId,
    };
    this.dataLines = [];
    this.eventName = '';
    return msg;
  }
}

export type StreamStatus = 'connecting' | 'connected' | 'reconnecting' | 'closed' | 'unauthorised';

export interface StreamState {
  status: StreamStatus;
  attempt: number;
  nextRetryAt: number | null;
  lastError: string | null;
  lastMessageAt: number | null;
}

export interface StreamOptions {
  url: string;
  getHeaders: () => Record<string, string>;
  onMessage: (msg: SseMessage) => void;
  onState: (state: StreamState) => void;
  /** Called on HTTP 401/403; the stream stops and does not retry. */
  onAuthFailure: (status: number) => void;
  fetchImpl?: typeof fetch;
  initialBackoffMs?: number;
  maxBackoffMs?: number;
  /** Reconnect if nothing (not even a heartbeat comment) arrives for this long. */
  idleTimeoutMs?: number;
}

export interface StreamHandle {
  close: () => void;
  retryNow: () => void;
}

export function backoffDelay(attempt: number, initialMs: number, maxMs: number, random = Math.random): number {
  const base = Math.min(maxMs, initialMs * 2 ** Math.max(0, attempt - 1));
  // ±20 % jitter so many tabs do not reconnect in lockstep.
  return Math.round(base * (0.8 + random() * 0.4));
}

export function connectStream(opts: StreamOptions): StreamHandle {
  const fetchImpl = opts.fetchImpl ?? fetch.bind(globalThis);
  const initial = opts.initialBackoffMs ?? 1000;
  const max = opts.maxBackoffMs ?? 30000;
  const idleTimeout = opts.idleTimeoutMs ?? 45000;

  const state: StreamState = { status: 'connecting', attempt: 0, nextRetryAt: null, lastError: null, lastMessageAt: null };
  let closed = false;
  let controller: AbortController | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let idleTimer: ReturnType<typeof setTimeout> | null = null;

  const emit = (patch: Partial<StreamState>) => {
    Object.assign(state, patch);
    opts.onState({ ...state });
  };

  const clearTimers = () => {
    if (retryTimer) clearTimeout(retryTimer);
    if (idleTimer) clearTimeout(idleTimer);
    retryTimer = null;
    idleTimer = null;
  };

  const armIdle = () => {
    if (idleTimer) clearTimeout(idleTimer);
    idleTimer = setTimeout(() => controller?.abort(new Error('idle timeout')), idleTimeout);
  };

  const scheduleRetry = (error: string) => {
    if (closed) return;
    const attempt = state.attempt + 1;
    const delay = backoffDelay(attempt, initial, max);
    emit({ status: 'reconnecting', attempt, lastError: error, nextRetryAt: Date.now() + delay });
    retryTimer = setTimeout(run, delay);
  };

  async function run(): Promise<void> {
    if (closed) return;
    clearTimers();
    controller = new AbortController();
    const parser = new SseParser();
    if (state.attempt === 0) emit({ status: 'connecting', nextRetryAt: null });
    let response: Response;
    try {
      response = await fetchImpl(opts.url, {
        headers: { Accept: 'text/event-stream', ...opts.getHeaders() },
        signal: controller.signal,
        cache: 'no-store',
      });
    } catch (err) {
      scheduleRetry(errorText(err));
      return;
    }
    if (response.status === 401 || response.status === 403) {
      closed = true;
      emit({ status: 'unauthorised', lastError: `HTTP ${response.status}`, nextRetryAt: null });
      opts.onAuthFailure(response.status);
      return;
    }
    if (!response.ok || !response.body) {
      scheduleRetry(`HTTP ${response.status}`);
      return;
    }

    emit({ status: 'connected', attempt: 0, nextRetryAt: null, lastError: null });
    armIdle();
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        armIdle();
        state.lastMessageAt = Date.now();
        for (const msg of parser.feed(decoder.decode(value, { stream: true }))) {
          opts.onMessage(msg);
        }
      }
      scheduleRetry('stream ended');
    } catch (err) {
      if (!closed) scheduleRetry(errorText(err));
    } finally {
      if (idleTimer) clearTimeout(idleTimer);
    }
  }

  void run();

  return {
    close() {
      closed = true;
      clearTimers();
      controller?.abort();
      emit({ status: 'closed', nextRetryAt: null });
    },
    retryNow() {
      if (closed || state.status !== 'reconnecting') return;
      clearTimers();
      void run();
    },
  };
}

function errorText(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}
