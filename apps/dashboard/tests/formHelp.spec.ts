import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import NumField from '@/components/config/NumField.vue';
import ExperimentConfigForm from '@/components/config/ExperimentConfigForm.vue';
import { createExperimentDraft, DRAFT } from '@/store/draft';
import { LAB_STORE } from '@/store/lab';
import { makeMeta } from './fixtures';
import { makeStore } from './helpers';

describe('form explanations', () => {
  it('keeps explanations visible alongside validation errors and links both to the input', async () => {
    const w = mount(NumField, { props: { label: 'Min', modelValue: 1, description: 'Fewest servers kept running.', hint: 'Approved maximum 5' } });
    await w.setProps({ error: 'Minimum must not exceed maximum' });
    const described = (w.find('input').attributes('aria-describedby') ?? '').split(' ').map((id) => w.find(`[id="${id}"]`).text());
    expect(described).toEqual(['Fewest servers kept running.', 'Approved maximum 5', 'Minimum must not exceed maximum']);
    expect(w.find('input').attributes('aria-invalid')).toBe('true');
    w.unmount();
  });

  it('explains every input for all traffic patterns and newly added categories and schedule points', async () => {
    const { store } = makeStore();
    store.meta.value = makeMeta();
    const draft = createExperimentDraft(store);
    const w = mount(ExperimentConfigForm, { global: { provide: { [LAB_STORE as symbol]: store, [DRAFT as symbol]: draft } } });
    await nextTick();
    for (const pattern of ['constant', 'ramp_up', 'ramp_down', 'spike', 'random_bursts', 'custom']) {
      await w.find('[data-testid="pattern-type"]').setValue(pattern);
      if (pattern === 'custom') await w.find('[data-testid="add-point"]').trigger('click');
      for (const field of w.findAll('[data-testid="form-field"]')) {
        const input = field.find('input, select');
        expect(input.exists()).toBe(true);
        const help = field.find('[data-testid="field-description"]');
        expect(help.text().length, field.find('label').text()).toBeGreaterThan(30);
        expect((input.attributes('aria-describedby') ?? '').split(' ')).toContain(help.attributes('id'));
      }
    }
    const categoryId = w.findAll('label').find((l) => l.text() === 'Categories')!.attributes('for');
    await w.find(`[id="${categoryId}"]`).setValue(4);
    const newPrice = w.findAll('[data-testid="form-field"]').find((f) => f.find('label').text() === 'Category 4')!;
    expect(newPrice.find('[data-testid="field-description"]').text()).toContain('No real money is charged');
    w.unmount();
  });
});
