import { config } from '@vue/test-utils';

// ECharts needs a real canvas; chart rendering is covered through the pure option builders instead.
config.global.stubs = {
  ...config.global.stubs,
  EChart: { props: ['option', 'height', 'label'], template: '<div class="echart-stub" :aria-label="label" />' },
};

// jsdom does not implement scrolling; the router's scrollBehavior calls it.
window.scrollTo = (() => undefined) as typeof window.scrollTo;
