import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import DestroyLabPanel from '@/components/teardown/DestroyLabPanel.vue';
import { mockFetch } from './helpers';

const phrase = 'destroy ticketing-scale-lab';
const status = { teardown: true, confirmPhrase: phrase, project: 'p', account: 'a@example.com' };
const idle = { state: 'idle', startedAt: null, finishedAt: null, exitCode: null, lines: [] };

afterEach(() => vi.unstubAllGlobals());

describe('Destroy lab panel', () => {
  it('is hidden when the dashboard is not opened through the local proxy', async () => {
    vi.stubGlobal('fetch', mockFetch({}));
    const w = mount(DestroyLabPanel);
    await flushPromises();
    expect(w.find('[data-testid="destroy-panel"]').exists()).toBe(false);
  });

  it('requires the exact phrase and sends it as the confirmation header', async () => {
    const posts: Array<Record<string, string>> = [];
    const fetch = mockFetch({
      'GET /local/status': { body: status },
      'GET /local/teardown': { body: idle },
      'POST /local/teardown': (req) => {
        posts.push(req.headers);
        return { status: 202, body: { ...idle, state: 'running', lines: ['### 1/6 Stopping any running traffic'] } };
      },
    });
    vi.stubGlobal('fetch', fetch);
    const w = mount(DestroyLabPanel, { attachTo: document.body });
    await flushPromises();

    await w.get('[data-testid="btn-destroy"]').trigger('click');
    const confirm = w.get('[data-testid="destroy-confirm"]');
    await w.get('[data-testid="destroy-phrase"]').setValue('destroy');
    expect(confirm.attributes('disabled')).toBeDefined();
    await w.get('[data-testid="destroy-phrase"]').setValue(phrase);
    expect(w.get('[data-testid="destroy-confirm"]').attributes('disabled')).toBeUndefined();

    await w.get('[data-testid="destroy-confirm"]').trigger('click');
    await flushPromises();
    expect(posts).toEqual([{ 'X-Lab-Confirm': phrase }]);
    expect(w.get('[data-testid="teardown-state"]').text()).toContain('Teardown running');
    expect(w.get('[data-testid="teardown-progress"] pre').text()).toContain('1/6 Stopping any running traffic');
    w.unmount();
  });

  it('reports completion from the script result, not on its own', async () => {
    vi.stubGlobal('fetch', mockFetch({
      'GET /local/status': { body: status },
      'GET /local/teardown': { body: { ...idle, state: 'failed', exitCode: 1, lines: ['xx Some lab resources still exist'] } },
    }));
    const w = mount(DestroyLabPanel);
    await flushPromises();
    expect(w.get('[data-testid="teardown-state"]').text()).toContain('failed (exit 1)');
  });
});
