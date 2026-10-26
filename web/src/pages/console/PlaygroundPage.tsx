// TODO(T19): implement PRD §5.8 with streaming output, real deduction and
// abort handling. Keys are selected server-side; never persist a plaintext key.
import { PageScaffold } from '@/components'

export default function PlaygroundPage() {
  return (
    <PageScaffold
      title="在线调试台"
      description="请求参数、流式输出与真实扣费结果同屏展示。"
      task="T19"
      sections={[
        '模型与 Key 选择',
        '请求参数编辑',
        '流式输出与中止',
        'usage 与扣费回显',
        '错误帧与请求 ID 展示',
      ]}
    />
  )
}
