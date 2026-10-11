import { afterEach, describe, expect, it, vi } from 'vitest';
import { backoffDelay, connectStream, SseParser, type StreamState } from '@/api/sse';

describe('SseParser', () => {
  it('parses a named event with JSON data', () => {
    const p = new SseParser();
    const msgs = p.feed('event: tick\ndata: {"a":1}\n\n');
    expect(msgs).toEqual([{ event: 'tick', data: '{"a":1}', id: null }]);
  });

  it('joins multi-line data with newlines and defaults the event name to "message"', () => {
    const p = new SseParser();
    expect(p.feed('data: line1\ndata: line2\ndata:line3\n\n')).toEqual([{ event: 'message', data: 'line1\nline2\nline3', id: null }]);
  });

  it('ignores comments such as the heartbeat ping', () => {
    const p = new SseParser();
    expect(p.feed(': ping\n\n')).toEqual([]);
    expect(p.feed(':\nevent: timeline\n: interleaved comment\ndata: {}\n\n')).toEqual([{ event: 'timeline', data: '{}', id: null }]);
  });

  it('handles chunks split mid-line and mid-event', () => {
    const p = new SseParser();
    expect(p.feed('event: ti')).toEqual([]);
    expect(p.feed('ck\ndata: {"x"')).toEqual([]);
    expect(p.feed(':2}\n')).toEqual([]);
    expect(p.feed('\n')).toEqual([{ event: 'tick', data: '{"x":2}', id: null }]);
  });

  it('accepts CRLF and CR line endings, including CRLF split across chunks', () => {
    const p = new SseParser();
    expect(p.feed('event: a\r\ndata: 1\r\n\r\n')).toEqual([{ event: 'a', data: '1', id: null }]);
    expect(p.feed('event: b\rdata: 2\r\r')).toEqual([{ event: 'b', data: '2', id: null }]);
    expect(p.feed('data: 3\r')).toEqual([]);
    expect(p.feed('\n\r\n')).toEqual([{ event: 'message', data: '3', id: null }]);
  });

  it('parses several events from one chunk and resets the event name between them', () => {
    const p = new SseParser();
    const msgs = p.feed('event: tick\ndata: 1\n\ndata: 2\n\nevent: experiment\ndata: 3\n\n');
    expect(msgs.map((m) => [m.event, m.data])).toEqual([
      ['tick', '1'],
      ['message', '2'],
      ['experiment', '3'],
    ]);
  });

  it('does not dispatch events without data, tracks id and retry, ignores unknown fields', () => {
    const p = new SseParser();
    expect(p.feed('event: nothing\n\n')).toEqual([]);
    expect(p.feed('id: 7\nretry: 5000\nfoo: bar\ndata\n\n')).toEqual([{ event: 'message', data: '', id: '7' }]);
    expect(p.retryMs).toBe(5000);
  });

  it('only strips a single leading space from the value', () => {
    const p = new SseParser();
    expect(p.feed('data:   indented\n\n')[0]?.data).toBe('  indented');
  });
});

describe('backoffDelay', () => {
  it('grows exponentially and is capped', () => {
    const mid = () => 0.5; // no jitter
    expect(backoffDelay(1, 1000, 30000, mid)).toBe(1000);
    expect(backoffDelay(2, 1000, 30000, mid)).toBe(2000);
    expect(backoffDelay(4, 1000, 30000, mid)).toBe(8000);
    expect(backoffDelay(10, 1000, 30000, mid)).toBe(30000);
  });
});

function streamResponse(chunks: string[], status = 200): Response {
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c));
      controller.close();
    },
  });
  return new Response(body, { status, headers: { 'Content-Type': 'text/event-stream' } });
}

describe('connectStream', () => {
  afterEach(() => vi.useRealTimers());

  it('sends the Authorization header and delivers parsed messages', async () => {
    const fetchImpl = vi.fn(async () => streamResponse(['event: tick\ndata: {"n":1}\n\n']));
    const messages: string[] = [];
    const handle = connectStream({
      url: '/api/stream',
      getHeaders: () => ({ Authorization: 'Bearer tok-123' }),
      onMessage: (m) => messages.push(`${m.event}:${m.data}`),
      onState: () => undefined,
      onAuthFailure: () => undefined,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    await vi.waitFor(() => expect(messages).toEqual(['tick:{"n":1}']));
    const init = (fetchImpl.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok-123');
    handle.close();
  });

  it('reconnects with backoff after the stream ends', async () => {
    vi.useFakeTimers();
    const fetchImpl = vi.fn(async () => streamResponse([': ping\n\n']));
    const states: StreamState[] = [];
    const handle = connectStream({
      url: '/api/stream',
      getHeaders: () => ({}),
      onMessage: () => undefined,
      onState: (s) => states.push(s),
      onAuthFailure: () => undefined,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      initialBackoffMs: 1000,
    });
    await vi.waitFor(() => expect(states.some((s) => s.status === 'reconnecting')).toBe(true));
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1300);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    handle.close();
  });

  it('stops and reports auth failure on 401 without retrying', async () => {
    vi.useFakeTimers();
    const fetchImpl = vi.fn(async () => new Response('', { status: 401 }));
    const onAuthFailure = vi.fn();
    connectStream({
      url: '/api/stream',
      getHeaders: () => ({}),
      onMessage: () => undefined,
      onState: () => undefined,
      onAuthFailure,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    await vi.waitFor(() => expect(onAuthFailure).toHaveBeenCalledWith(401));
    await vi.advanceTimersByTimeAsync(60000);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });
});
