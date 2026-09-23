/**
 * WS 保活实证脚本：连上后端 → hello → welcome → subscribe → 收到 ping 回 pong，
 * 然后站着不动，看连接能活多久。
 *
 * 修复前：服务端读超时 = PongTimeoutMs(3s)×(MaxMissedPong+1) = 12s，
 *         而心跳周期 15s，客户端还没等到第一个 ping 就被判死 → 精确 12s 断开。
 * 修复后：读超时 = 心跳周期(15s)×(3+1) = 60s，且每次上行消息顺延 → 应长期存活。
 *
 * 用法：node scripts/ws-keepalive-probe.mjs [端口] [观测秒数]
 */
// Node 22 自带全局 WebSocket，无需第三方依赖
const WebSocket = globalThis.WebSocket

const port = Number(process.argv[2] ?? 9080)
const observeSec = Number(process.argv[3] ?? 40)
const url = `ws://127.0.0.1:${port}/api/v1/ws`

const t0 = Date.now()
const el = () => ((Date.now() - t0) / 1000).toFixed(1).padStart(5)

let closedAt = null
let pingCount = 0
let pongSent = 0

const ws = new WebSocket(url)

ws.addEventListener('open', () => {
  console.log(`${el()}s  连接已建立`)
  // 协议：op 字段（不是 type）；必须一开连就发 hello
  ws.send(JSON.stringify({ op: 'hello', protocol: 1, ref: 'r-hello' }))
})

ws.addEventListener('message', (ev) => {
  let msg
  try { msg = JSON.parse(String(ev.data)) } catch { return }
  switch (msg.op) {
    case 'welcome':
      console.log(`${el()}s  收到 welcome（connId=${msg.connId}）→ 发 subscribe`)
      ws.send(JSON.stringify({ op: 'subscribe', channelIds: [], ref: 'r-sub' }))
      break
    case 'subscribed':
      console.log(`${el()}s  收到 subscribed`)
      break
    case 'ping':
      pingCount++
      ws.send(JSON.stringify({ op: 'pong', t: msg.t }))
      pongSent++
      if (pingCount <= 5) console.log(`${el()}s  收到第 ${pingCount} 个 ping → 已回 pong`)
      break
    case 'error':
      console.log(`${el()}s  服务端 error: ${JSON.stringify(msg)}`)
      break
  }
})

ws.addEventListener('close', (ev) => {
  if (closedAt === null) closedAt = Date.now()
  console.log(`${el()}s  连接被关闭 code=${ev.code} reason=${ev.reason || '（无）'}`)
})

ws.addEventListener('error', (ev) => console.log(`${el()}s  error: ${ev.message ?? ev.type}`))

setTimeout(() => {
  const alive = closedAt === null
  console.log('───────────────────────────────────────────')
  console.log(`观测时长   : ${observeSec}s`)
  console.log(`连接仍存活 : ${alive ? '✅ 是' : `❌ 否（存活 ${((closedAt - t0) / 1000).toFixed(1)}s）`}`)
  console.log(`收到 ping  : ${pingCount} 次，已回 pong ${pongSent} 次`)
  console.log(alive && observeSec > 20
    ? '结论：保活正常，12s 断连循环已消除'
    : '结论：仍在断连，需继续排查')
  try { ws.close() } catch {}
  process.exit(alive ? 0 : 1)
}, observeSec * 1000)
