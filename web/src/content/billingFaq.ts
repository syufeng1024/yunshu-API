export interface BillingFaqItem {
  id: string
  question: string
  answer: string
}

/**
 * Single copy source for the pricing-page FAQ and /docs chapter 7 (F4-5
 * spirit: one wording, two render locations).
 */
export const BILLING_FAQ: readonly BillingFaqItem[] = [
  {
    id: 'credit-calculation',
    question: '积分怎么算？',
    answer:
      '文本请求按输入 token 与输出 token 分别乘以对应单价后求和；单价以「积分/千 token」展示。金额以整数微积分运算，账单中的积分与人民币按平台统一比例换算。',
  },
  {
    id: 'failed-request',
    question: '失败请求是否收费？',
    answer:
      '未产生输出的失败请求不扣积分。流式请求在首字节之后上游失败时，已输出部分按实际用量计费，错误帧会带本次实际扣费；未输出部分不计。',
  },
  {
    id: 'recharge',
    question: '如何充值？',
    answer:
      '登录控制台，在充值中心选择套餐并完成支付。充值积分与赠送积分按两个批次入账，赠送积分有独立有效期，不随套餐权益到期而顺延。',
  },
  {
    id: 'invoice',
    question: '如何开票？',
    answer:
      '在控制台提交开票申请（抬头 / 税号 / 邮箱），运营核验后人工开具，并回填发票单号与文件。V1.0 不提供自动开票与红冲。',
  },
  {
    id: 'raise-limit',
    question: '能否提额？',
    answer:
      '可在控制台提交提额申请。当前所有有效套餐的限流规格取最高值，不叠加；提额结果以审核通过后的配置为准。',
  },
]
