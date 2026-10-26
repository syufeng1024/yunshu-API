import type { IconName } from '@/lib/constants'

export interface Capability {
  icon: IconName
  title: string
  description: string
}

/** The six capabilities required by PRD §4.1. Copy stays mechanism-level. */
export const CAPABILITIES: readonly Capability[] = [
  {
    icon: 'grid',
    title: '多厂商聚合',
    description: '统一入口接入多家境内上游，按模型别名解析到实际渠道，客户端不需要分别配置厂商。',
  },
  {
    icon: 'plug',
    title: '协议兼容',
    description: '同时提供 OpenAI 兼容与 Anthropic 两条接入协议，路径、请求头与响应结构按协议分别实现。',
  },
  {
    icon: 'terminal',
    title: '智能路由',
    description: '按渠道权重、健康状态与故障转移规则选择候选链路，每次尝试单独记录耗时与结果。',
  },
  {
    icon: 'shield',
    title: '四维限流',
    description: 'RPM、TPM、并发与账户日额度分别计数，触顶时返回具体维度与恢复时刻。',
  },
  {
    icon: 'wallet',
    title: '精确计费',
    description: '金额以整数微积分运算，请求级唯一舍入点；输入与输出分别计价，账单可逐条复核。',
  },
  {
    icon: 'chart',
    title: '全链路日志',
    description: '渠道、耗时、重试次数与错误码写入请求日志，按 request_id 关联预扣、结算与上游尝试。',
  },
]
