import type { ComposeOption } from 'echarts/core';
import type { LineSeriesOption } from 'echarts/charts';
import type {
  DataZoomComponentOption,
  GridComponentOption,
  LegendComponentOption,
  MarkLineComponentOption,
  MarkPointComponentOption,
  TooltipComponentOption,
} from 'echarts/components';

export type ChartOption = ComposeOption<
  | LineSeriesOption
  | GridComponentOption
  | TooltipComponentOption
  | LegendComponentOption
  | MarkLineComponentOption
  | MarkPointComponentOption
  | DataZoomComponentOption
>;

export type LineSeries = LineSeriesOption;

/** Single colour vocabulary shared by charts, badges and cards. */
export const C = {
  text: '#cbd5e1',
  muted: '#94a3b8',
  grid: '#1e293b',
  axis: '#334155',
  target: '#e2e8f0',
  achieved: '#38bdf8',
  http: '#a78bfa',
  ok: '#34d399',
  error: '#f87171',
  dropped: '#fbbf24',
  business: '#facc15',
  healthy: '#34d399',
  starting: '#fbbf24',
  draining: '#fb923c',
  unhealthy: '#f87171',
  unknown: '#94a3b8',
  limit: '#64748b',
  approved: '#f472b6',
  p50: '#38bdf8',
  p95: '#a78bfa',
  p99: '#f472b6',
  available: '#34d399',
  held: '#fbbf24',
  sold: '#38bdf8',
  expired: '#fb923c',
  rejected: '#f87171',
  autoscaler: '#38bdf8',
  baseline: '#a78bfa',
  operator: '#fbbf24',
} as const;

/** Extra colours for per-instance CPU lines. */
export const INSTANCE_PALETTE = ['#38bdf8', '#a78bfa', '#34d399', '#fbbf24', '#f472b6', '#2dd4bf', '#fb923c', '#818cf8', '#a3e635', '#e879f9'];

export function baseOption(yName: string, opts: { yMax?: number; yFormatter?: (v: number) => string; rightAxis?: string } = {}): ChartOption {
  const axis = (name: string, align: 'left' | 'right', split: boolean, max?: number, formatter?: (v: number) => string) => ({
    type: 'value' as const,
    name,
    nameTextStyle: { color: C.muted, align },
    min: 0,
    max,
    axisLabel: { color: C.muted, formatter },
    splitLine: { show: split, lineStyle: { color: C.grid } },
    axisLine: { lineStyle: { color: C.axis } },
  });
  const yAxis = [axis(yName, 'left', true, opts.yMax, opts.yFormatter)];
  if (opts.rightAxis) yAxis.push(axis(opts.rightAxis, 'right', false));
  return {
    backgroundColor: 'transparent',
    useUTC: true,
    animation: false,
    textStyle: { color: C.text, fontFamily: 'inherit' },
    grid: { left: 8, right: opts.rightAxis ? 8 : 16, top: 56, bottom: 8, containLabel: true },
    legend: { type: 'scroll', top: 0, textStyle: { color: C.text, fontSize: 11 }, pageTextStyle: { color: C.muted }, itemWidth: 14, itemHeight: 8 },
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#0f172a',
      borderColor: '#334155',
      textStyle: { color: '#e2e8f0', fontSize: 12 },
      axisPointer: { type: 'line', lineStyle: { color: '#475569' } },
      valueFormatter: (v) => (v == null || v === '-' ? 'N/A' : typeof v === 'number' ? String(Math.round(v * 100) / 100) : String(v)),
    },
    xAxis: {
      type: 'time',
      axisLabel: { color: C.muted, hideOverlap: true },
      axisLine: { lineStyle: { color: C.axis } },
      splitLine: { show: false },
    },
    yAxis,
  };
}

export function line(name: string, color: string, data: Array<[number, number | null]>, extra: Partial<LineSeries> = {}): LineSeries {
  return {
    type: 'line',
    name,
    data,
    showSymbol: false,
    connectNulls: false,
    lineStyle: { width: 1.6, color },
    itemStyle: { color },
    emphasis: { focus: 'series' },
    ...extra,
  };
}

export function dashed(name: string, color: string, data: Array<[number, number | null]>, extra: Partial<LineSeries> = {}): LineSeries {
  return line(name, color, data, { lineStyle: { width: 1.4, color, type: 'dashed' }, ...extra });
}
