// TODO(T19): implement PRD §5.6. Amounts and bonus credits come from the plan
// API; the bundle must not contain any concrete price or quota literal.
import { PageScaffold } from '@/components'

export default function RechargePage() {
  return (
    <PageScaffold
      title="充值中心"
      description="充值积分与套餐赠送积分分账展示，退款只涉及未消耗的充值积分。"
      task="T19"
      sections={[
        '充值金额与到账积分',
        '套餐购买（一次性购买，不做订阅）',
        '充值积分 / 赠送积分分账',
        '订单记录与状态',
        '退款与开票入口',
        '支付回调结果提示',
      ]}
    />
  )
}
