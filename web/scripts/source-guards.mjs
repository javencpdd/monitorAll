/**
 * 源码级回归守卫（已入库，CI 会跑）。
 *
 * 为什么需要它：本文件守的每一条都是 `npx vue-tsc` 与 `go vet` **抓不到**的缺陷 ——
 * 它们在类型上完全合法，只在运行期出问题，而且多数表现为「静默失败」。
 * 历史战绩：MapNodeInspector 兄弟节点 inject runtime（面板静默消失一整个版本）、
 * loadSources 导出但零调用（数据源列表恒空）、卡片拖拽坐标空间错配（锁死在节点右下）。
 * 加新断言时请沿用同一风格：改回旧写法必须立刻变红。
 *
 * 为什么需要它：`npx vue-tsc` **抓不到**这次这个 bug —— 兄弟组件里调 `useMapRuntime()`
 * 类型完全合法，只在运行期 inject 失败并抛异常（还被 Vue 吞掉）。
 * 所以用源码级断言把这条不变量钉住，防止下次有人"顺手"再把 runtime 用回兄弟组件。
 *
 * 覆盖：
 *   A. MapNodeInspector 零 `useMapRuntime`
 *   B. 所有 `useMapRuntime` 消费者都**不是** `.ma-view__stage` 的直接子组件（即确实在 surface 子树内）
 *   C. MapRuntime 接口不含 previewPoint；预览点读写两侧都走 store
 *   D. main.ts：errorHandler 在 mount 之前赋值、带 info、带去重限流；两个 window 监听仍在
 *   I. loadSources 在 store 之外必须有调用点（防「导出了但没人调用」→ 列表恒空）
 *   J. ensureAllChannels 自愈；SourcePanel 空列表三态（不许把「加载失败」说成「暂无数据源」）
 *   K. MapCardShell 交给拖拽的矩形必须是**容器绝对坐标**（否则 clamp 会把卡片锁死在节点右下）
 *   L. 后端日志点：可能带内联凭据的 URL 必须经 logx.RedactURL 脱敏后才能落日志
 *   M. 已知泄露的真实凭据不得出现在源码路径中；.Trash-* 不得被跟踪
 *   N. 数据源只读详情：URL 凭据脱敏存在且被模板包裹使用；SourcePanel 有详情入口
 *   O. 向导：推流模式(publish)不下发 / 不渲染流地址；Step2 是只读摘要（不再渲染表单字段）
 *   P. MediaMTX 路径必须有格式校验；自动解析要能覆盖非法值（否则整条 rtmp:// 会落库）
 *   Q. MediaMTX 路径校验的**行为**口径：用真实导出的 MEDIA_MTX_PATH_RE + normalizeMediaMtxPath
 *      跑代表性用例（而不是抄一份正则来测）；并断言向导提交的正是归一化后的值
 *   R. 视频异常诊断链路：后端 VideoPayload 必须带 Detail/PublishURL 且 emitStatus 真的填充；
 *      前端失败态必须消费 detail（优先诊断、回落通用文案），推流地址可一键复制，
 *      拉流上游地址必须脱敏后展示（RTSP 内联口令不得明文上屏）
 */
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const WEB = new URL('..', import.meta.url).pathname
const SRC = join(WEB, 'src')

let pass = 0
const total = 18
function check(name, fn) {
  try {
    fn()
    pass += 1
    console.log(`PASS  ${name}`)
  } catch (e) {
    console.log(`FAIL  ${name}\n      ${e.message}`)
    process.exitCode = 1
  }
}

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}
const FILES = walk(SRC)
const read = (p) => readFileSync(p, 'utf8')

/** PascalCase 文件名 → 组件标签名（本项目模板一律用 kebab）。 */
function tagOf(file) {
  const base = file.split('/').pop().replace(/\.vue$/, '')
  return base.replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase()
}

/* —— 取 `.ma-view__stage` 的直接子组件清单（缩进法，够稳） —— */
const viewSrc = read(join(SRC, 'views/MapBoardView.vue'))
const viewLines = viewSrc.split('\n')
const stageStart = viewLines.findIndex((l) => l.includes('class="ma-view__stage"'))
assert.ok(stageStart >= 0, 'MapBoardView 里找不到 .ma-view__stage')
const stageIndent = viewLines[stageStart].match(/^\s*/)[0]
const stageEnd = viewLines.findIndex((l, i) => i > stageStart && l === `${stageIndent}</div>`)
const stageBlock = viewLines.slice(stageStart + 1, stageEnd >= 0 ? stageEnd : undefined)
const stageChildren = stageBlock
  .filter((l) => l.match(/^\s*/)[0].length === stageIndent.length + 2 && l.trim().startsWith('<'))
  .map((l) => (l.trim().match(/^<([a-z][a-z0-9-]*)/) ?? [])[1])
  .filter(Boolean)

/** 所有调用 useMapRuntime 的文件 → 组件标签。 */
const runtimeConsumers = FILES.filter((f) => f.endsWith('.vue') && /useMapRuntime\s*\(/.test(read(f))).map(
  (f) => ({ file: f.replace(`${SRC}/`, ''), tag: tagOf(f) }),
)

check('A. MapNodeInspector.vue 零 useMapRuntime（连注释都不含该 token）', () => {
  const s = read(join(SRC, 'components/mapboard/MapNodeInspector.vue'))
  const hits = s.split('\n').filter((l) => l.includes('useMapRuntime'))
  assert.equal(hits.length, 0, `仍有 ${hits.length} 处命中：${hits.join(' | ')}`)
  assert.ok(
    !s.includes("from '@/composables/useMapProjection'"),
    'MapNodeInspector 仍在 import useMapProjection',
  )
})

check('B. stage 直接子组件 = [map-board-surface, map-node-inspector, …]（证明二者是兄弟）', () => {
  assert.ok(stageChildren.includes('map-board-surface'), `stage 子组件里没有 map-board-surface：${stageChildren}`)
  assert.ok(stageChildren.includes('map-node-inspector'), `stage 子组件里没有 map-node-inspector：${stageChildren}`)
  console.log(`      stage 直接子组件：${stageChildren.join(', ')}`)
})

check('C. useMapRuntime 的消费者全部不是 stage 的直接子组件（⇒ 都在 surface 子树内）', () => {
  assert.ok(runtimeConsumers.length >= 4, `消费者数量异常：${runtimeConsumers.length}`)
  for (const c of runtimeConsumers) {
    assert.ok(
      !stageChildren.includes(c.tag),
      `${c.file}（<${c.tag}>）是 stage 直接子组件 —— 它调 useMapRuntime 会在 setup 抛异常并静默消失`,
    )
    console.log(`      <${c.tag}>  (${c.file}) ✓ 非 stage 直接子级`)
  }
})

check('D. MapRuntime 接口已不含 previewPoint', () => {
  const s = read(join(SRC, 'composables/useMapProjection.ts'))
  const body = s.slice(s.indexOf('export interface MapRuntime {'), s.indexOf('export const MapRuntimeKey'))
  assert.ok(body.length > 0, '取 MapRuntime 接口体失败')
  assert.ok(!/^\s*previewPoint/m.test(body), 'MapRuntime 接口里仍有 previewPoint 字段')
})

check('E. 预览点读写两侧都走 store', () => {
  const dot = read(join(SRC, 'components/mapboard/MapPreviewDot.vue'))
  assert.ok(dot.includes('store.previewPoint'), 'MapPreviewDot 未从 store 读 previewPoint')
  assert.ok(!/runtime\.previewPoint/.test(dot), 'MapPreviewDot 仍在读 runtime.previewPoint')

  const insp = read(join(SRC, 'components/mapboard/MapNodeInspector.vue'))
  assert.equal((insp.match(/store\.setPreviewPoint\(/g) ?? []).length, 4, 'MapNodeInspector 的 setPreviewPoint 调用数应为 4')
  assert.ok(!/runtime\.previewPoint/.test(insp), 'MapNodeInspector 仍在写 runtime.previewPoint')

  const surface = read(join(SRC, 'components/mapboard/MapBoardSurface.vue'))
  assert.ok(!/const previewPoint = shallowRef/.test(surface), 'MapBoardSurface 仍持有本地 previewPoint')
  assert.ok(!/^\s*previewPoint,$/m.test(surface), 'MapBoardSurface 仍把 previewPoint 塞进 runtime 字面量')
  assert.ok(surface.includes('store.previewPoint'), 'MapBoardSurface 未从 store 读 previewPoint')
})

check('F. store 侧 previewPoint 定义 / setter / hydrate+reset 两处清理 / 导出齐全', () => {
  const s = read(join(SRC, 'stores/mapBoard.ts'))
  assert.ok(s.includes('const previewPoint = shallowRef<LngLat | null>(null)'), '缺 previewPoint 定义')
  assert.ok(s.includes('function setPreviewPoint('), '缺 setPreviewPoint')
  assert.ok(/import type \{ LngLat \} from '@\/utils\/geo'/.test(s), 'LngLat 不是 type-only import')
  const clears = (s.match(/previewPoint\.value = null/g) ?? []).length
  // 清空点应正好 2 处：hydrate() 与 reset()（team-lead 指定的两处）。
  // setter 是 `= v`，不计入；组件卸载时的清理在 MapNodeInspector 里走 setPreviewPoint(null)。
  assert.equal(clears, 2, `previewPoint 清空点应为 2（hydrate/reset），实际 ${clears}`)
  for (const fn of ['function hydrate(', 'function reset(']) {
    const at = s.indexOf(fn)
    assert.ok(at >= 0, `找不到 ${fn}`)
    const body = s.slice(at, at + 4000)
    const clearAt = body.search(/previewPoint\.value = null/)
    const unplacedAt = body.search(/unplacedNodeIds\.value = new Set\(\)/)
    assert.ok(clearAt >= 0, `${fn} 里没有清空 previewPoint`)
    assert.ok(unplacedAt >= 0, `${fn} 里没有清空 unplacedNodeIds`)
    assert.ok(
      Math.abs(clearAt - unplacedAt) < 400,
      `${fn} 里 previewPoint 的清理没有紧挨着其它清理（应与 unplacedNodeIds/nodeCrsSource 同一处）`,
    )
  }
  assert.ok(/^\s*previewPoint,$/m.test(s), 'previewPoint 未导出')
  assert.ok(/^\s*setPreviewPoint,$/m.test(s), 'setPreviewPoint 未导出')
})

const mainSrc = read(join(SRC, 'main.ts'))

check('G. main.ts：errorHandler 在 mount 之前赋值、带 info、带去重限流', () => {
  const ehIdx = mainSrc.indexOf('app.config.errorHandler')
  const mountIdx = mainSrc.indexOf("app.mount('#app')")
  assert.ok(ehIdx >= 0, '没有 app.config.errorHandler')
  assert.ok(mountIdx >= 0, '没有 app.mount')
  assert.ok(ehIdx < mountIdx, 'errorHandler 必须在 mount 之前赋值，否则早期异常收不到')
  assert.ok(mainSrc.includes('console.error(\'[vue error]\''), '缺少 console.error 出口')
  assert.ok(mainSrc.includes('${info}'), '提示文案未带 info（无法定位是 setup 还是 render）')
  // 去重限流三要素：时间常量 + Map 记录 + 时间差比较
  assert.ok(/VUE_ERROR_DEDUP_MS\s*=\s*[\d_]+/.test(mainSrc), '缺少去重时间常量')
  assert.ok(/new Map<string, number>\(\)/.test(mainSrc), '缺少去重记录 Map')
  assert.ok(/now - \(lastVueErrorAt\.get\(key\) \?\? 0\) < VUE_ERROR_DEDUP_MS/.test(mainSrc), '缺少去重比较')
  assert.ok(/lastVueErrorAt\.set\(key, now\)/.test(mainSrc), '未记录上次提示时间')
  assert.ok(mainSrc.includes('lastVueErrorAt.clear()'), '缺少 Map 体积保护')
})

check("H. main.ts：原有两个 window 监听未被破坏", () => {
  assert.ok(mainSrc.includes("window.addEventListener('error'"), 'window error 监听丢失')
  assert.ok(mainSrc.includes("window.addEventListener('unhandledrejection'"), 'unhandledrejection 监听丢失')
})

check('I. loadSources 在 store 之外必须有调用点（防「导出但没人调用」）', () => {
  const callers = walk(SRC)
    .filter((f) => !f.endsWith('stores/datasource.ts'))
    .filter((f) => readFileSync(f, 'utf-8').includes('loadSources'))
    .map((f) => f.replace(`${WEB}/`, ''))
  assert.ok(callers.length > 0,
    'loadSources 在 stores/datasource.ts 之外零调用点 —— 数据源列表永远不会被加载，' +
    '面板只显示「本次会话新建过的源」，用户会误以为没建成功而反复新建')
  console.log(`      调用点: ${callers.join(', ')}`)
})

check('J. ensureAllChannels 自愈 + SourcePanel 空列表三态', () => {
  const ds = readFileSync(join(SRC, 'stores/datasource.ts'), 'utf-8')
  assert.ok(
    /async function ensureAllChannels\(\): Promise<void> \{[\s\S]*?if \(sources\.value\.length === 0\) await loadSources\(\)/.test(ds),
    'ensureAllChannels 没有在 sources 为空时补拉 loadSources —— 它会「成功地什么都不做」',
  )
  const sp = readFileSync(join(SRC, 'components/layout/SourcePanel.vue'), 'utf-8')
  assert.ok(sp.includes('datasourceStore.loading'), '空列表没有 loading 态')
  assert.ok(sp.includes('datasourceStore.error'),
    '空列表没有错误态 —— 加载失败时会显示「暂无数据源」，用户会以为真没有然后重复新建')
  assert.ok(/重试/.test(sp), '错误态缺少重试出口')
})

check('K. 卡片拖拽矩形必须是容器绝对坐标（readRect 不能直接返回 geom 相对偏移）', () => {
  const shell = readFileSync(join(SRC, 'components/mapboard/MapCardShell.vue'), 'utf-8')
  const readRect = /function readRect\(\): DragRect \{([\s\S]*?)\n\}/.exec(shell)
  assert.ok(readRect, '找不到 readRect')
  const body = readRect[1]
  assert.ok(!/return \{ \.\.\.geom\.value \}/.test(body),
    'readRect 直接返回了 geom.value —— 那是【相对节点锚点的偏移】，而 bounds 是容器尺寸，' +
    'clampToBounds 会用容器原点夹相对量，导致 x>=0/y>=0：卡片被锁死在节点右下方，放不到左边也放不到上边')
  assert.ok(/anchorBase\(\)/.test(body), 'readRect 没有经 anchorBase() 换算成容器绝对坐标')
  assert.ok(/base\.x \+ geom\.value\.x/.test(body) && /base\.y \+ geom\.value\.y/.test(body),
    'readRect 没有把锚点基准与相对偏移相加')
  // commit 必须把绝对坐标减回相对量，否则存进 map_geom 的就是错的空间
  const commit = /function commit\(r: DragRect\): void \{([\s\S]*?)\n\}/.exec(shell)
  assert.ok(commit, '找不到 commit')
  assert.ok(/r\.x - dragBase\.x/.test(commit[1]) && /r\.y - dragBase\.y/.test(commit[1]),
    'commit 没有把绝对坐标减掉 dragBase 换回相对偏移 —— map_geom 会存进错误坐标系的值')
  // preview 直接写绝对坐标，不能再叠一次 base（叠了就是双倍偏移）
  assert.ok(!/base\.x \+ r\.x/.test(shell), 'preview 仍在 base + r.x 双叠偏移')
})

check('L. 后端日志点：带凭据的 URL 必须经 logx.RedactURL 脱敏', () => {
  // 这些是唯一会把"数据源地址"写进日志的位置。历史上 logs/monitorall.log 里
  // 出现过 59 次明文摄像头口令，就是因为它们原样记录了 rtsp://admin:pw@host/...
  const sites = [
    ['internal/adapter/video/video.go', '视频适配器已启动', 'source'],
    ['internal/adapter/http/adapter.go', 'HTTP 轮询失败', 'url'],
    ['internal/adapter/ros/client.go', 'rosbridge 已连接', 'url'],
    ['internal/adapter/ros/client.go', 'rosbridge 连接断开', 'url'],
  ]
  const SERVER = new URL('../../server', import.meta.url).pathname
  for (const [file, msg, field] of sites) {
    const src = readFileSync(join(SERVER, file), 'utf-8')
    const line = src.split('\n').find((l) => l.includes(msg))
    assert.ok(line, `${file} 里找不到日志「${msg}」—— 位置变了请同步更新本守卫`)
    assert.ok(line.includes('logx.RedactURL('),
      `${file} 的「${msg}」没有经 logx.RedactURL 脱敏 —— 内联凭据会原样落日志`)
    assert.ok(new RegExp(`"${field}",\\s*logx\\.RedactURL\\(`).test(line),
      `${file} 的「${msg}」字段 ${field} 没有包在 RedactURL 里`)
  }
})

check('M. 已知泄露凭据不得出现在源码中；.Trash-* 不得被跟踪', () => {
  // 2026-10-13 安全事件：摄像头口令 okwy1688 曾以硬编码 placeholder / 测试值 /
  // 文档示例的形式散布在 6 个源文件里，并随构建产物进入 git 历史（远程仓库是公开的）。
  const ROOT = new URL('../..', import.meta.url).pathname
  const LEAKED = 'okwy1688'
  const dirs = ['web/src', 'server', 'scripts', 'docs', 'note', 'deploy']
  const skip = new Set(['node_modules', '.Trash-0', 'dist', '.git'])
  for (const d of dirs) {
    const base = join(ROOT, d)
    if (!existsSync(base)) continue
    for (const f of walk(base)) {
      // 守卫自身合法地包含检测串（LEAKED 常量），必须排除
      if (f.endsWith('source-guards.mjs')) continue
      if ([...skip].some((s) => f.includes(`/${s}/`))) continue
      let src
      try {
        src = readFileSync(f, 'utf-8')
      } catch {
        continue // 读不了的文件（如 600 权限的 secret.key）跳过，不让守卫本身崩掉
      }
      assert.ok(!src.includes(LEAKED),
        `${f.replace(`${ROOT}/`, '')} 里出现了已泄露的真实凭据 —— 请改成虚构示例值`)
    }
  }
  // .Trash-* 曾把 191 个旧构建产物（含明文凭据）提交进历史；.gitignore 必须挡住它
  const gi = readFileSync(join(ROOT, '.gitignore'), 'utf-8')
  assert.ok(gi.includes('.Trash-*'),
    '.gitignore 缺少 .Trash-*/ —— 垃圾目录会被再次提交（历史上就是这么泄的）')
  // 编译产物同样是泄露载体：二进制内嵌旧 bundle 就会带上旧口令，改完源码必须重新构建
  const bin = join(ROOT, 'server/bin/monitorall')
  if (existsSync(bin)) {
    const buf = readFileSync(bin, 'latin1')
    assert.ok(!buf.includes(LEAKED),
      'server/bin/monitorall 里仍含已泄露凭据 —— 源码已改，必须重新 npm run build + 嵌入 + make build')
  }
})

check('N. 数据源详情：脱敏存在且被使用；详情抽屉有入口；模板不得裸插值 URL', () => {
  // ① 脱敏工具存在，且实现里含针对 userinfo 的正则与 `:***@` 替换结果
  const redactPath = join(SRC, 'utils/redact.ts')
  assert.ok(existsSync(redactPath), '缺少 utils/redact.ts —— URL 内联凭据没有打码工具')
  const redact = read(redactPath)
  const fnAt = redact.indexOf('export function maskUrlCredentials')
  assert.ok(fnAt >= 0, 'utils/redact.ts 没有导出 maskUrlCredentials')
  const fnBody = redact.slice(fnAt)
  assert.ok(fnBody.includes('.replace('), 'maskUrlCredentials 没有做替换')
  assert.ok(fnBody.includes(':***@'), 'maskUrlCredentials 的替换结果里没有 `:***@`（密码没被打码）')
  assert.ok(fnBody.includes(':\\/\\/') || /\\\\s/.test(fnBody) || fnBody.includes('[^@'),
    'maskUrlCredentials 没有针对 `://user:pass@` 的 userinfo 正则')

  // ② 详情抽屉导入了脱敏函数
  const drawerPath = join(SRC, 'components/config/DataSourceDetailDrawer.vue')
  assert.ok(existsSync(drawerPath), '缺少 DataSourceDetailDrawer.vue')
  const drawer = read(drawerPath)
  assert.ok(
    /import\s*\{[^}]*maskUrlCredentials[^}]*\}\s*from\s*'@\/utils\/redact'/.test(drawer),
    '详情抽屉没有从 @/utils/redact 导入 maskUrlCredentials',
  )

  // ③ 模板里对 rtmpUrl / url 的插值必须包在脱敏函数里（不得裸插值）
  const tplStart = drawer.indexOf('<template>')
  const tplEnd = drawer.lastIndexOf('</template>')
  assert.ok(tplStart >= 0 && tplEnd > tplStart, '详情抽屉取不到 <template> 块')
  const tpl = drawer.slice(tplStart, tplEnd)
  const interps = [...tpl.matchAll(/\{\{([\s\S]*?)\}\}/g)].map((m) => m[1])
  const naked = interps.filter((expr) => /\b(rtmpUrl|url)\b/.test(expr) && !expr.includes('maskUrlCredentials('))
  assert.equal(naked.length, 0,
    `模板里有未脱敏的 URL 插值（会明文显示密码）：${naked.join(' | ')}`)
  const wrapped = interps.filter((expr) => /\b(rtmpUrl|url)\b/.test(expr) && expr.includes('maskUrlCredentials('))
  assert.ok(wrapped.length >= 1,
    '模板里没有任何"被脱敏包裹的 URL 插值" —— 脱敏可能没真正接上（本守卫会空转）')

  // ④ SourcePanel 有打开详情抽屉的入口
  const sp = read(join(SRC, 'components/layout/SourcePanel.vue'))
  assert.ok(/openDetail\s*\(/.test(sp), 'SourcePanel 没有打开详情抽屉的入口')
  assert.ok(
    sp.includes('DataSourceDetailDrawer') || sp.includes('data-source-detail-drawer'),
    'SourcePanel 没有挂载 DataSourceDetailDrawer',
  )
})

check('O. 向导：推流模式不下发/不渲染流地址；Step2 是只读摘要（不再渲染表单）', () => {
  const wiz = read(join(SRC, 'components/config/DataSourceWizard.vue'))

  // ① video 分支里 rtmpUrl 不得**无条件**下发 —— 必须是条件展开（推流模式/空地址不下发）
  // 注意：`if (kind.value === 'video')` 也出现在 urlError 里，必须锚到 connParams 之后再取。
  const cpAt = wiz.indexOf('const connParams')
  const vAt = wiz.indexOf("if (kind.value === 'video')", cpAt)
  const rAt2 = wiz.indexOf("if (kind.value === 'ros')", vAt)
  assert.ok(cpAt >= 0 && vAt > cpAt && rAt2 > vAt, '取不到 connParams 的 video 分支')
  const videoBranch = wiz.slice(vAt, rAt2)
  assert.ok(
    !/\brtmpUrl:\s*rtmpUrl\.value\.trim\(\)/.test(videoBranch),
    'video 分支仍无条件下发 rtmpUrl —— 推流模式会落库一条不参与行为的流地址（用户真实踩过）',
  )
  assert.ok(
    /\.\.\.[\s\S]{0,140}?isPublish\.value[\s\S]{0,140}?rtmpUrl:/.test(videoBranch),
    'video 分支没有以"条件展开"下发 rtmpUrl —— 推流模式/空地址不应下发该键',
  )

  // ② Step1 承载可编辑表单，且流地址输入行被 !isPublish 包住（推流模式不渲染）
  const s1At = wiz.indexOf('class="ma-wizard__step1"')
  const revAt = wiz.indexOf('class="ma-wizard__review"')
  assert.ok(s1At >= 0 && revAt > s1At, '取不到 Step1/Step2 容器')
  const step1 = wiz.slice(s1At, revAt)
  assert.ok(/<input\s+v-model="name"/.test(step1), 'Step1 里没有名称输入 —— 参数无处可填')
  assert.ok(/<input\s+v-model="rtmpUrl"/.test(step1), 'Step1 里没有流地址输入 —— 拉流模式无法填地址')
  assert.ok(
    /<template v-if="!isPublish">/.test(step1),
    '流地址输入行没有被 <template v-if="!isPublish"> 包起来 —— 推流模式仍会渲染它',
  )

  // ③ Step2 必须是只读摘要：块内不得出现任何表单控件 / v-model
  const footAt = wiz.indexOf('<footer', revAt)
  assert.ok(footAt > revAt, '取不到 Step2 review 块末端')
  const review = wiz.slice(revAt, footAt)
  assert.ok(/class="ma-wizard__summary"/.test(review), 'Step2 缺少只读摘要块（ma-wizard__summary）')
  assert.ok(
    !/<input\b/.test(review) && !/<select\b/.test(review) && !/<textarea\b/.test(review),
    'Step2 摘要区里仍有表单控件（input/select/textarea）—— 用户会以为要重填一遍',
  )
  assert.ok(!/v-model/.test(review), 'Step2 摘要区里仍有 v-model（表单字段）')
})

check('P. MediaMTX 路径必须有格式校验；自动解析要能覆盖非法值', () => {
  // ① 常量里有"合法字符集"白名单（与 MediaMTX 服务端规则逐字对齐）
  const consts = read(join(SRC, 'utils/constants.ts'))
  const mAt = consts.indexOf('MEDIA_MTX_PATH_RE')
  assert.ok(mAt >= 0, 'constants.ts 没有定义 MEDIA_MTX_PATH_RE —— 路径格式校验无处可依')
  const decl = consts.slice(mAt, mAt + 120)
  assert.ok(
    /\^\[A-Za-z0-9_/.test(decl),
    'MEDIA_MTX_PATH_RE 不是"合法字符集白名单"写法（应形如 /^[A-Za-z0-9_.\\-\\/]+$/）',
  )

  const wiz = read(join(SRC, 'components/config/DataSourceWizard.vue'))

  // ② 向导使用了该白名单，并显式识别"整条地址"这个最高频错误
  assert.ok(
    wiz.includes('MEDIA_MTX_PATH_RE'),
    '向导没有对 mediaMtxPath 做格式校验 —— 用户会把 rtmp:// 整条地址填进路径字段，' +
      'MediaMTX 返回 invalid path name，前端却表现为「HLS 协商超时」，提示完全误导',
  )
  assert.ok(
    /includes\(':\/\/'\)\s*\)\s*\{/.test(wiz) && /indexOf\(':\/\/'\)\s*\+\s*3/.test(wiz),
    '纠错逻辑没有识别"整条地址"（含 :// 并取主机后的路径部分）—— 报错文案就无法告诉用户该怎么改',
  )
  assert.ok(
    /MediaMTX 路径只需流名（如 live\/cam1），不能填完整地址/.test(wiz),
    '缺少"只需流名、不能填完整地址"的可执行报错文案（应与后端 ValidateMediaMTXPath 一致）',
  )
  assert.ok(
    /function suggestMediaMTXPath\(/.test(wiz) && /function pathError\(/.test(wiz),
    '缺少 suggestMediaMTXPath（纠错建议）或 pathError（格式校验）',
  )

  // ③ video 分支的 formError 必须接入该校验（否则拦不住提交）
  const feAt = wiz.indexOf('const formError')
  const cpAt = wiz.indexOf('const connParams')
  assert.ok(feAt >= 0 && cpAt > feAt, '取不到 formError 块')
  const feBlock = wiz.slice(feAt, cpAt)
  assert.ok(
    /if \(mtxPathError\.value\) return mtxPathError\.value/.test(feBlock),
    'video 分支的 formError 没有接入 MediaMTX 路径格式校验 —— 非法值拦不住，仍会落库',
  )

  // ④ 自动解析必须能覆盖非法值：非空**且合法**才保留。
  //    ⚠️ 只断言行为、不断言写法：这里换过实现（从 pathError(x)==='' 变成 mtxPathError.value===''），
  //    写死其中一种都会造成假红 —— 守卫要拦的是"逻辑退化"，不是"没按我写的抄"。
  const wAt = wiz.indexOf('watch(rtmpUrl')
  assert.ok(wAt >= 0, '找不到"从流地址自动解析路径"的 watch')
  const wBlock = wiz.slice(wAt, wAt + 600)
  assert.ok(
    !/if \(mediaMtxPath\.value\.trim\(\)\.length > 0\) return/.test(wBlock),
    '自动解析仍是"非空就不覆盖" —— 会永久保护住用户误填的非法路径（如 rtmp://host/live/x），' +
      '之后改流地址也纠正不过来',
  )
  const keepsOnlyWhenValid =
    /pathError\(mediaMtxPath\.value\)\s*===\s*''/.test(wBlock) || /mtxPathError\.value\s*===\s*''/.test(wBlock)
  assert.ok(
    keepsOnlyWhenValid,
    '自动解析没有按"非空且合法才保留"判断（既没有 pathError(...) 与空串比较，也没有 mtxPathError.value 与空串比较）',
  )

  // ⑤ 前后端正则必须**逐字一致**：否则同一条数据被两套规则解读，
  //    表现就是"前端放行了、后端 40001 拒绝"——又一次静默不一致。
  const SERVER = new URL('../../server', import.meta.url).pathname
  const beSrc = readFileSync(join(SERVER, 'internal/media/mediamtx.go'), 'utf-8')
  const beRe = /regexp\.MustCompile\(`([^`]+)`\)/.exec(beSrc)
  assert.ok(beRe, '后端 internal/media/mediamtx.go 里取不到 mediaMTXPathRe —— 口径对齐断言失效')
  const feRe = /export const MEDIA_MTX_PATH_RE = \/(.+)\/$/m.exec(consts)
  assert.ok(feRe, '前端 constants.ts 里取不到 MEDIA_MTX_PATH_RE')
  // 前端正则字面量里 `/` 要写成 `\/`，比较前先归一化
  const normalize = (s) => s.replace(/\\\//g, '/')
  assert.equal(
    normalize(feRe[1]),
    beRe[1],
    `前后端 MediaMTX 路径正则不一致（前 ${normalize(feRe[1])} / 后 ${beRe[1]}）` +
      ' —— 会出现"前端放行、后端 40001 拒绝"的静默失败；改任一侧都必须同步另一侧',
  )
  console.log(`      前后端正则逐字一致：${beRe[1]}`)
})

check('Q. MediaMTX 路径校验行为口径：归一化 + 白名单判对了（不是抄一份正则来测）', () => {
  // 用 esbuild 把真实的 TS 常量打成一个临时 CJS 包，在仓库外 require。
  // 为什么值得多这一步：文本比对（守卫 P 的 ⑤）只能证明"两边长得一样"，
  // 既证明不了"判对了"，也会因为等价写法不同（如 [0-9A-Za-z] 换序）而假红。
  // 这里测的是**真实导出**，抄一份正则来测等于没测。
  const outfile = join(tmpdir(), `ma-path-guard-${process.pid}.cjs`)
  const esbuildBin = join(WEB, 'node_modules/esbuild/bin/esbuild')
  assert.ok(existsSync(esbuildBin), `取不到 esbuild：${esbuildBin}`)
  try {
    execFileSync(esbuildBin, ['--bundle', '--format=cjs', '--platform=node', `--outfile=${outfile}`, join(SRC, 'utils/constants.ts')], {
      stdio: 'pipe',
    })
    const req = createRequire(import.meta.url)
    const { MEDIA_MTX_PATH_RE, normalizeMediaMtxPath } = req(outfile)

    // ① 归一化：前导斜杠是自然手误（从地址栏复制的习惯），必须被纠正而不是报错
    const normCases = [
      ['/live/cam1', 'live/cam1'],
      ['live/cam1/', 'live/cam1'],
      ['  live/cam1  ', 'live/cam1'],
      ['///live/cam1///', 'live/cam1'],
      ['live/cam1', 'live/cam1'],
      ['', ''],
    ]
    for (const [input, want] of normCases) {
      assert.equal(
        normalizeMediaMtxPath(input),
        want,
        `normalizeMediaMtxPath(${JSON.stringify(input)}) 应为 ${JSON.stringify(want)}`,
      )
    }

    // ② 该放行的必须放行（含"前导斜杠归一化后合法"这一条 —— 正是用户踩的坑）
    const accept = ['live/cam1', 'cam1', 'live/m20pro-f', 'a_b.c-d/1', '/live/cam1', 'live/whip1']
    for (const p of accept) {
      const n = normalizeMediaMtxPath(p)
      assert.ok(
        n.length > 0 && MEDIA_MTX_PATH_RE.test(n),
        `合法路径 ${JSON.stringify(p)}（归一化 ${JSON.stringify(n)}）被拒 —— 会误伤正常配置`,
      )
    }

    // ③ 该拒绝的必须拒绝。这些值若漏过去，前端放行、后端 40001 拒，
    //    而 Step2 不显示 formError、错误只靠 toast 一闪 → 用户体感"点了没反应"。
    const reject = [
      'rtmp://172.31.68.227:1936/live/m20pro-f',
      'rtsp://admin:pw@host:554/Streaming/Channels/101',
      'live//cam1',
      'live/cam1?token=x',
      'live/cam1#frag',
      'live/cam 1',
      'live/cam1:8080',
      'http://host:8888/live/x',
      'rtmp:host/live/x',
    ]
    for (const p of reject) {
      const n = normalizeMediaMtxPath(p)
      assert.ok(
        !MEDIA_MTX_PATH_RE.test(n),
        `非法路径 ${JSON.stringify(p)}（归一化 ${JSON.stringify(n)}）竟然通过白名单 —— 会落到后端 40001`,
      )
    }
    console.log(`      归一化 ${normCases.length} 例 / 放行 ${accept.length} 例 / 拒绝 ${reject.length} 例`)
  } finally {
    rmSync(outfile, { force: true })
  }

  // ④ 提交的值必须是归一化后的：否则"校验用的值"与"发出去的值"不是同一个，
  //    前端放行 / 后端照样拒（这次事故的完整形状）。
  const wiz = read(join(SRC, 'components/config/DataSourceWizard.vue'))
  assert.ok(/const mtxPathNorm = computed/.test(wiz), '向导缺少 mtxPathNorm（归一化后的路径）')
  const cpAt = wiz.indexOf('const connParams')
  assert.ok(cpAt >= 0, '取不到 connParams')
  const cpBlock = wiz.slice(cpAt, cpAt + 900)
  assert.ok(
    /params\.mediaMtxPath = mtxPathNorm\.value/.test(cpBlock),
    'connParams 没有提交归一化后的路径 —— 校验与提交用了两个不同的值',
  )
  assert.ok(
    !/params\.mediaMtxPath = mediaMtxPath\.value\.trim\(\)/.test(cpBlock),
    'connParams 仍在提交未归一化的原始值',
  )

  // ⑤ 校验本身也必须走归一化。少了这一步：`/live/cam1` 会被前端判非法（报错），
  //    而 connParams 提交的却是归一化后的合法值 —— 用户被一个本该自动纠正的
  //    手误挡住，且提示与"该填什么"对不上。
  const peAt = wiz.indexOf('function pathError(')
  assert.ok(peAt >= 0, '取不到 pathError')
  const peBlock = wiz.slice(peAt, peAt + 900)
  assert.ok(
    /const v = normalizeMediaMtxPath\(path\)/.test(peBlock),
    'pathError 没有先归一化再校验 —— 前导斜杠这种自然手误会被误判为非法',
  )
  assert.ok(
    !/const v = path\.trim\(\)/.test(peBlock),
    'pathError 又退回了"只 trim 不归一化"的写法',
  )
})

check('R. 视频异常诊断链路：后端下发 detail/publishUrl，前端失败态消费（含脱敏与复制）', () => {
  // 背景：MediaMTX 管理 API 只回 ready=false、不带原因，真实错误（dial tcp ... refused）
  // 只存在于 MediaMTX 日志里。这条链断在任一环，用户看到「HLS 协商超时」都不知道该查
  // 推流端还是查本机服务 —— 必须后端填、前端演，两侧同时钉住。
  const SERVER = new URL('../../server', import.meta.url).pathname

  // ① 后端 VideoPayload 必须有 Detail / PublishURL，json tag 与前端逐字一致
  //    （tag 漂移 = 后端下发、前端按旧结构解析后静默丢失，detail 永远是 undefined）
  const payloadGo = readFileSync(join(SERVER, 'internal/model/payload.go'), 'utf-8')
  const vpAt = payloadGo.indexOf('type VideoPayload struct')
  assert.ok(vpAt >= 0, '后端取不到 VideoPayload')
  const vpBody = payloadGo.slice(vpAt, payloadGo.indexOf('\n}', vpAt))
  assert.ok(
    /Detail\s+string\s+`json:"detail,omitempty"`/.test(vpBody),
    '后端 VideoPayload 缺 Detail string `json:"detail,omitempty"`',
  )
  assert.ok(
    /PublishURL\s+string\s+`json:"publishUrl,omitempty"`/.test(vpBody),
    '后端 VideoPayload 缺 PublishURL string `json:"publishUrl,omitempty"`',
  )

  // ② 前端类型同步：字段名与可选性与后端一一对应
  const types = read(join(SRC, 'types/index.ts'))
  const feVpAt = types.indexOf('export interface VideoPayload')
  assert.ok(feVpAt >= 0, '前端取不到 VideoPayload')
  const feVp = types.slice(feVpAt, types.indexOf('\n}', feVpAt))
  assert.ok(/detail\?: string/.test(feVp), '前端 VideoPayload 缺 detail?: string')
  assert.ok(/publishUrl\?: string/.test(feVp), '前端 VideoPayload 缺 publishUrl?: string')

  // ③ emitStatus 必须真的填充 Detail、并调 BuildPublishURLs —— 否则字段永远是空串，守卫空转
  const videoGo = readFileSync(join(SERVER, 'internal/adapter/video/video.go'), 'utf-8')
  const esAt = videoGo.indexOf('func (a *Adapter) emitStatus')
  assert.ok(esAt >= 0, '取不到 emitStatus')
  const esEnd = videoGo.indexOf('\n}', esAt)
  assert.ok(esEnd > esAt, '取不到 emitStatus 函数体末端')
  const esBody = videoGo.slice(esAt, esEnd)
  assert.ok(
    /Detail:/.test(esBody) || /\.Detail\s*=/.test(esBody),
    'emitStatus 没有填充 Detail —— 后端下发的诊断永远是空，前端只能展示通用文案',
  )
  assert.ok(
    /BuildPublishURLs\(/.test(esBody),
    'emitStatus 没有调 BuildPublishURLs —— publish 模式拿不到「该推到哪」的地址',
  )
  // ③b Detail 严禁内联源 URL：RTSP 地址带内联口令（rtsp://admin:pw@host/...），
  //     拼进诊断文案等于把口令随视频帧群发（守卫 L 同口径：凭据不得落进日志/帧）。
  for (const line of esBody.split('\n')) {
    if (!/Detail/.test(line)) continue
    assert.ok(
      !/info\.Raw|sourceURL/.test(line),
      `Detail 相关行内联了源 URL（凭据泄露风险）：${line.trim()}`,
    )
  }

  // ④ 前端失败态必须消费 detail：优先诊断、回落通用文案；且不得再写死 description
  const vr = read(join(SRC, 'components/renderers/VideoRenderer.vue'))
  assert.ok(/payload\.value\?\.detail/.test(vr), 'VideoRenderer 没有读 payload.detail —— 后端诊断无人消费')
  assert.ok(/const failureDetail = computed/.test(vr), 'VideoRenderer 缺 failureDetail 计算属性')
  assert.ok(
    /:description="failureDetail"/.test(vr),
    '失败态 empty-state 没有把 description 绑到 failureDetail —— detail 展示不出去',
  )
  assert.ok(
    !/description="可尝试手动切换协议/.test(vr),
    'description 又写死了通用文案 —— 后端下的诊断会被忽略，退回到「没有任何提示」的状态',
  )
  assert.ok(
    /可尝试手动切换协议，或检查推流端与 MediaMTX 是否在线/.test(vr),
    '通用兜底文案丢失 —— detail 为空的异常会没有说明文字',
  )

  // ⑤ publish 模式：卡片上直接给「请推流至 <地址> + 复制」按钮。
  //    推流方最常犯的错就是推到别的 IP / 用错流名，摆出来比任何报错文案都根治。
  const tplStart = vr.indexOf('<template>')
  const tplEnd = vr.lastIndexOf('</template>')
  assert.ok(tplStart >= 0 && tplEnd > tplStart, '取不到 VideoRenderer 的 <template> 块')
  const tpl = vr.slice(tplStart, tplEnd)
  assert.ok(
    /@click="copy\(publishUrl\)"/.test(tpl),
    '失败态没有「复制推流地址」按钮 —— 推流方仍要猜 IP/端口/流名',
  )
  assert.ok(/\{\{\s*publishUrl\s*\}\}/.test(tpl), '推流地址没有展示出来（只给复制按钮不给可见地址）')

  // ⑤b embedded 模式必须把 publishUrl 的 host 改写成页面 host（与播放地址同一口径）：
  //     后端 lanHost 是「按网卡字典序探测的第一个私有 IPv4」，实测为 VPN 网卡 10.42.0.1，
  //     推流方根本够不到；external 模式（MediaMTX 在别的主机）绝不改写。
  const puAt = vr.indexOf('const publishUrl = computed')
  assert.ok(puAt >= 0, '取不到 publishUrl 计算属性')
  const puBody = vr.slice(puAt, puAt + 900)
  assert.ok(
    /replaceUrlHost\(/.test(puBody),
    'publishUrl 没有经 replaceUrlHost 改写 —— 卡片会展示后端探测到的 VPN 网卡地址，推流方够不到',
  )
  assert.ok(
    /embedded !== true\)\s*return raw/.test(puBody),
    'publishUrl 的改写没有 embedded 门禁 —— external 模式（MediaMTX 在别的主机）会被错误改写',
  )
  assert.ok(
    /import\s*\{[^}]*replaceUrlHost[^}]*\}\s*from\s*'@\/utils\/video'/.test(vr),
    'VideoRenderer 没有从 @/utils/video 导入 replaceUrlHost',
  )
  const uv = read(join(SRC, 'utils/video.ts'))
  assert.ok(
    /export function replaceUrlHost\(/.test(uv),
    'utils/video.ts 没有导出 replaceUrlHost —— 组件里会另抄一份 URL 解析，两版必然漂移',
  )

  // ⑥ 拉流模式：上游地址必须脱敏后展示（RTSP 内联口令不得明文上屏）
  assert.ok(
    /const maskedSourceUrl = computed/.test(vr) && /maskUrlCredentials\(/.test(vr),
    'VideoRenderer 缺 maskedSourceUrl（或没有过 maskUrlCredentials）—— 上游地址会带明文口令上屏',
  )
  assert.ok(/\{\{\s*maskedSourceUrl\s*\}\}/.test(tpl), '脱敏后的上游地址没有展示')
  for (const m of tpl.matchAll(/\{\{([\s\S]*?)\}\}/g)) {
    assert.ok(
      !/\bsourceUrl\b/.test(m[1]),
      `模板里直接插值了未脱敏的 sourceUrl：${m[1].trim()}`,
    )
  }

  // ⑦ 复制必须走 utils/clipboard：用户是跨主机 http 访问看板（非安全上下文），
  //    navigator.clipboard 在那里不存在，只有该工具有 execCommand 兜底。
  assert.ok(
    /import\s*\{\s*copyText\s*\}\s*from\s*'@\/utils\/clipboard'/.test(vr),
    'VideoRenderer 没有从 @/utils/clipboard 导入 copyText —— 非安全上下文下复制必失败',
  )
})

console.log(`\n${pass}/${total} 断言通过`)