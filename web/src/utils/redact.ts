/**
 * 前端脱敏工具：展示层用。
 *
 * ⚠️ 为什么前端必须自己做这一层：
 *   后端 `DataSource.connParams` 的注释说「敏感键已被替换为 `***`」，
 *   但那**只覆盖 `secretKeys`**（即名为 `password` / `token` 的**独立字段**）。
 *   它**不会**处理 URL 内联的 userinfo，例如摄像头地址：
 *       rtsp://admin:example-pw@192.168.2.69:554/Streaming/Channels/101
 *   （示例口令为虚构值；真实凭据不得写进源码，见 scripts/source-guards.mjs 守卫 M）
 *   这条 URL 是**一个字符串字段**，密码内联其中，后端整体原样返回。
 *   所以任何把这串 URL 画到界面上的地方，都必须先过这里打码。
 *
 * 只做一件事：把 `scheme://user:password@host` 里的 **password 换成 `***`**，
 * 用户名保留（用户需要看出"这是谁的账号"）。
 */

/**
 * 打码 URL 内联凭据中的密码。
 *
 * 用**正则**而不是 `new URL()`：
 *   `rtsp` / `rtmp` 这类 scheme 在 URL API 里的解析行为在不同环境下并不一致
 *   （部分实现会把它们当作非特殊 scheme，userinfo 解析结果不稳定），
 *   而正则对这三个协议的形状（`scheme://user:pass@host`）足够稳。
 *
 * 行为（与守卫断言逐条对齐）：
 *   - `rtsp://admin:example-pw@192.168.2.69:554/x` → `rtsp://admin:***@192.168.2.69:554/x`（用户名保留）
 *   - `rtmp://172.31.68.227:1936/live/lite3`    → 原样（无 userinfo）
 *   - `rtsp://admin@host/x`                       → 原样（只有用户名、没有密码）
 *   - `''` / 非字符串                              → 原样返回（UI 里不要因解析失败而藏掉信息）
 *
 * 容错优先：任何"看起来不像 URL"的输入一律原样返回，绝不抛异常、绝不吞掉内容 ——
 * 展示层宁可显示得朴素，也不能因为脱敏把用户要看的信息弄丢。
 */
export function maskUrlCredentials(url: string): string {
  if (typeof url !== 'string' || url.length === 0) return url
  // `(://user)` 保留 scheme + 用户名；只把 `:password@` 里的密码段替换为 `***`
  return url.replace(/:\/\/([^:/@\s]+):([^@/\s]+)@/, '://$1:***@')
}
