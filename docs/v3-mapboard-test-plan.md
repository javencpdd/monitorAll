# MonitorAll v3「地图看板（MapBoard）」测试策略与验收方案

> 文档性质：**只写方案，不修改任何源码**。
> 目标读者：研发、测试、发布负责人。本文档同时是「可执行的验收剧本」。
> 所有命令默认在 `/home/jack/monitorAll` 根目录下执行；涉及 Go 测试的都带齐 `PATH` 与 `GOTMPDIR` 前置。

---

## 0. 环境硬约束（本方案的前提）

本机**无法做浏览器自动化**：headless Chrome 会因 `ptrace` 崩溃、Playwright 超时（详见 `note/06-调试方法论.md`）。因此：

1. **前端交互类验收只能靠「静态分析 + 构建产物核对 + 请用户人工验证」三件套**。本文档不写任何"用 Playwright 点一下"这种跑不了的用例。
2. `/tmp` 是 10M tmpfs，跑 Go 测试必须 `export GOTMPDIR="$HOME/.cache/monitorall/tmp"`，否则报 `no space left on device`。Go 在 `/usr/local/go/bin`，不在默认 `PATH`。
3. 真库是 `/home/jack/monitorAll/data/monitorall.db`，**绝对禁止拿它做测试**。任何涉及写的验证必须先 `cp` 到临时目录（见 §8 开头）。
4. 前端无 vitest/jsdom，但 `web/node_modules` 与 `web/dist` 已存在，`npm run build`（vue-tsc + vite build）可真实执行；纯逻辑模块可在 node 下跑（见 §3.2）。

---

## 1. 测试范围与不在范围内

### 在范围内（本次验收要答到）

- v3 地图看板后端：节点 CRUD、补丁语义、乐观锁（revision CAS）、级联解绑、几何端点、坐标/CRS 校验、迁移链（v2→v3）、外键与 CHECK 约束。
- 前端核心逻辑（纯函数 / store 状态机）：投影器数学、坐标系转换（`geo.ts`）、视口剔除、节点状态机（stale / 收到过不再回退兜底）、几何草稿与防回环。
- 前端改动「真的进了产物」的构建核对。
- 相邻 surface：全局设置（品牌 + 天气）接口冒烟、Logo 上传安全、runtime 信息泄露。

### 不在范围内（明确不验，及原因）

| 不验项 | 原因 |
|---|---|
| 高德底图真实瓦片渲染、样式切换肉眼效果、WebGL 上下文、暗色/卫星视觉 | 需真浏览器 + 真 AMap Key + GPU，本环境跑不了且无法自动化（note/04 §4.2） |
| 真实 GPS/ROS 设备端到端定位精度 | 依赖外设与现场，不能用单元测试替代 |
| 20 节点 × 10Hz 实时压测下的帧率/内存 | 属性能验收，需真浏览器 + 真数据，另行排期 |
| 用户主观体验（布局美观、拖拽手感） | 主观，只能人工走查 |
| 移动端 WebView / 低版本浏览器兼容 | 环境无设备，且 note/04 已说明"浏览器版本过低"是误导性文案，属已知风险 |

---

## 2. 测试分层策略（无浏览器自动化下）

### 2.1 后端单元 / 集成（Go test）—— 主力自动层

能覆盖：模型校验（`ValidateMapNode` / `ValidateGeometry` / `ValidateNodeChannel`）、补丁合并（`mergeMapNodePatch`）、乐观锁（`bumpMapBoardRevision` 的 SQL CAS）、级联解绑（`DeleteMapNode`）、删通道节点处置（`DetachMapNodesByChannel`）、几何端点、迁移链、外键与 CHECK。

运行命令（带齐前置）：

```bash
export PATH="$PATH:/usr/local/go/bin"
export GOTMPDIR="$HOME/.cache/monitorall/tmp"
mkdir -p "$GOTMPDIR"
cd /home/jack/monitorAll/server
go test ./internal/store/... ./internal/api/... ./internal/model/... \
  -run 'Map|MapBoard|MapNode|Geo|Settings|Weather|Migrat|Dashboard' \
  -count=1 -v 2>&1 | tee /tmp/mbtest-go.log
```

> 现有测试已覆盖大量回归（见 §6 与文末「覆盖缺口」），本文档重点标出**缺口**并给出应补的用例。

### 2.2 前端纯逻辑（node 下可跑）—— 次主力自动层

`web/src/utils/geo.ts`、`web/src/utils/projector.ts`（含 `toWorld`/`fromWorld`/`projectToContainer`/`unprojectFromContainer`/`metersPerPixel`/`VirtualProjector`）、`web/src/composables/useViewportCull.ts` 的 `intersects`/`evaluate` 逻辑，**不依赖 Vue 运行时或 DOM**，可在 node 下跑。

可行性结论（已核实）：`geo.ts` 仅依赖 `gcoord`（纯 JS，ESM）；`projector.ts` 的共享数学是无副作用纯函数；`useViewportCull` 的 `evaluate` 只在最后调用 `window.setTimeout`，纯判定部分可抽取单测，但 `window` 引用需用 `globalThis` 垫片或 esbuild 定义 `window`。

跑法（用已安装的 esbuild 临时打包成 ESM 再 node 执行）：

```bash
cd /home/jack/monitorAll/web
# 例：把 geo.ts 打成 node 可执行的 mjs
npx esbuild src/utils/geo.ts --bundle --format=esm --platform=node \
  --outfile=/tmp/mbtest/geo.mjs
node --input-type=module -e '
import { toGCJ02, fromGCJ02, isValidLatLonRange } from "/tmp/mbtest/geo.mjs";
// 北京天安门 WGS-84 -> GCJ-02 应明显偏移（几百米量级）
const [x,y] = toGCJ02(116.397428, 39.90923, "WGS-84");
console.log("GCJ-02偏移(度):", x-116.397428, y-39.90923);
// 逆变换应回原值
const [a,b] = fromGCJ02(x,y,"WGS-84");
console.log("往返误差(度):", a-116.397428, b-39.90923);
console.log("isValidLatLonRange(91,0)=", isValidLatLonRange(91,0)); // false
'
```

```bash
# 例：投影器数学往返（projector.ts 只打包纯函数部分）
npx esbuild src/utils/projector.ts --bundle --format=esm --platform=node \
  --outfile=/tmp/mbtest/proj.mjs
node --input-type=module -e '
import { toWorld, fromWorld, worldPxAtZoom, projectToContainer, unprojectFromContainer } from "/tmp/mbtest/proj.mjs";
const z=12, size={w:1280,h:800}, center=[116.397428,39.90923];
const worldPx=worldPxAtZoom(z);
// 容器中心应映射回 center 本身
const c = projectToContainer(center, center, z, size);
console.log("center->container(应≈640,400):", c.x.toFixed(2), c.y.toFixed(2));
// 经纬度->容器->经纬度 往返应一致
const p = projectToContainer([116.41,39.92], center, z, size);
const ll = unprojectFromContainer(p, center, z, size);
console.log("往返误差:", ll[0]-116.41, ll[1]-39.92);
'
```

> 若团队后续要常态化，建议在 `web` 加最小 `vitest` 配置只跑 `utils/`、`composables/useViewportCull` 的判定纯函数（Vue 相关 composable 仍不进测试）。本文档不强制加依赖，给出零依赖的 esbuild 跑法作为落地手段。

### 2.3 构建产物核对 —— 验证「前端改动真的生效」的主力手段

为什么必须做（note/06 §6.1/§6.4）：改了代码但用户没重启/没强刷，是本项目"修复无效"的最常见原因。构建产物核对能证明新代码进了 dist，且无浏览器也能跑。

```bash
cd /home/jack/monitorAll/web
npm run build                       # vue-tsc --noEmit && vite build
ls dist/assets/                     # 确认最新 hash 文件名（每次构建都变）
# 核对 v3 地图看板相关字符串确实进入产物（已核实当前 dist 均命中）
for s in "__maMap" "ma-drawer__body" "fallbackLat" "channelMissing" "amap://styles/dark" "mapboard"; do
  echo "$s -> $(grep -ro --include=*.js --include=*.css "$s" dist/assets | wc -l)"
done
# 自洽性：index.html 引用的 entry chunk 必须真实存在于 dist/assets
grep -o 'assets/[A-Za-z0-9_-]*\.js' dist/index.html
ls dist/assets/*.js | grep -o '[A-Za-z0-9_-]*\.js'
# note/03 §3.6 修复：配置抽屉子项必须禁用收缩（scoped CSS 的属性选择器插在中间）
grep -o 'ma-drawer__body\[data-v-[a-z0-9]*\]>[^{]*{[^}]*flex-shrink:0[^}]*}' dist/assets/*.css
# 坐标系转换唯一入口（合规 C-05）：全仓 gcoord 调用应只命中 geo.ts 被打进的 chunk
grep -ro 'gcoord' dist/assets | wc -l
```

判定：以上探针字符串计数 >0、index.html 引用的 entry 在 dist 中存在、`flex-shrink:0` 规则存在 → 前端改动已落地。计数从 0 变正数，即「新功能进了产物」的证据。

### 2.4 必须请用户人工验证的清单（可照做步骤）

每条给出「前置 → 操作 → 期望」，用户照做即可，不写"检查地图是否正常"这类空话。

- **M-人工-1 底图与节点渲染**：前置=已配 `server/config.yaml` 的 `amapKey`/`amapSecurityCode` 并重启。操作=打开一个地图看板，建一个绑定 geo_pose 通道的节点。期望=高德底图出现、节点圆点落在通道实时位置上、拖拽地图所有节点/卡片整组跟随不漂移。
- **M-人工-2 坐标系偏差肉眼法**：前置=节点表单填经纬度 + 坐标系选 `BD-09`。操作=保存后在地图上用「表单预览点」看落点。期望=选错 CRS 时点明显偏出（境内 300~700 米），改回 `WGS-84` 后回到正确位置（note/04 §4.4）。
- **M-人工-3 暗色样式**：操作=切 `amap://styles/dark`。期望=底图变暗；若不变，按 note/04 §4.2 四步查（样式值合法？配了 securityCode？启用 WebGL？`__maMap.getMapStyle()` 返回什么）。控制台出现"浏览器版本过低"多半是 WebGL 未启用，与浏览器新旧无关。
- **M-人工-4 卫星图层**：操作=开启卫星。期望=卫星是**叠加图层**而非样式，关闭后回到暗色底图（不残留卫星盖住暗色，note/04 §4.3）。
- **M-人工-5 无 Key 降级**：操作=清空 `amapKey` 重启。期望=地图看板降级为「坐标列表 + 提示」，绝不切到任何境外底图（合规红线 note/04 §4.7）。
- **M-人工-6 节点编辑不丢字段**：操作=编辑一个节点只改「坐标系」，保存。期望=名字、兜底坐标、通道绑定都原样保留（补丁语义，§4 R14 例外）。
- **M-人工-7 删节点卡片保留**：操作=删除一个挂了卡片的节点（默认 unbind）。期望=卡片变成未锚定（灰显/可重绑），不消失；删节点选 cascade=delete 时卡片才被删。
- **M-人工-8 视口剔除稳定**：操作=把某节点移出视口边缘再移回，反复蹭。期望=卡片隐藏/恢复不抖动、不反复拉断流（稳定态 flips=0，P-06）；视频卡恢复延迟 ≤1.5s。
- **M-人工-9 断流不瞬移**：操作=节点收到过坐标后停止推流。期望=节点停留在最后已知位置并标 stale，**不跳回兜底坐标**（A10）。
- **M-人工-10 全局设置**：操作=改品牌标题/副标题、上传 Logo(png/jpg/webp)、开天气填深圳南山坐标、刷新页面。期望=标题/Logo 生效、天气挂件显示「雷/雨」等中文、刷新首屏不闪默认 MonitorAll；删除 Logo 后回落纯文字。

---

## 3. 回归用例清单

> 来源列：是哪个历史 pitfall（note/0x）或哪条契约（代码）。
> 自动化可行性：**可自动**（Go 单测/集成、或 node 纯函数单测能跑）/ **半自动**（需临时实例+脚本、WS 探针、构建产物核对、静态 grep）/ **只能人工**（必须浏览器）。
> 「缺口」标记表示当前仓库**还没有**对应测试，应补。

| 编号 | 来源 | 前置 | 步骤 | 期望 | 自动化 |
|---|---|---|---|---|---|
| R01 | note/04 §4.4 + projector.ts | 无 | 对 `projectToContainer`/`unprojectFromContainer` 做往返；`toWorld`/`fromWorld` 边界 lat=±85.05 | 容器中心映射回 center；经纬度往返误差 <1e-9；超纬被钳制 | 可自动 |
| R02 | note/04 §4.4 + geo.ts | 无 | `toGCJ02(lon,lat,'WGS-84')` vs `'GCJ-02'` vs `'BD-09'`；`fromGCJ02` 往返 | WGS-84 与 GCJ-02 偏差为数百米量级；BD-09 更大；往返误差≈0 | 可自动 |
| R03 | note/04 §4.3 | 无 | 前端常量里搜 `satellite` 作 `setMapStyle` 参数 | 不出现 `amap://styles/satellite` 作样式；卫星只用 `TileLayer.Satellite` | 半自动（静态 grep + 构建产物） |
| R04 | note/04 §4.2 | 有 Key 浏览器 | 切暗色四步排查 | 见 M-人工-3 | 只能人工 |
| R05 | note/04 §4.1 + C5 | 无 Key | 读前端 `projectorKind`/`mapStatus` 经接口或状态 | 无 Key 时 `projectorKind='virtual'`、`mapStatus='fallback'`，不切境外底图 | 半自动 |
| R06 | note/04 + api C11 | geo 通道 + map 渲染器 | `POST /dashboards/:id/cards` 带 `rendererType:map` 且属地图看板 | 40003（已覆盖 `TestCreateCardRejectsMapRendererInMapBoard`） | 可自动 |
| R07 | note/03 §3.1 | 构建产物 | 静态查 `<script setup>` 局部变量名是否与组件驼峰标签撞车（gridLayout/gridItem/cardHost/mapBoardSurface） | 无撞车；否则生产构建静默吞子树 | 半自动（grep 源 + 构建产物行为） |
| R08 | note/03 §3.3 + store R1 | 无 | `commitGeometry` 值相同不排队；`flushGeometry` 响应不回写 `geomDraft` | 相同值不入队、不发请求；无自激振荡 | 可自动（store 单测） |
| R09 | note/03 §3.6 | 构建产物 | grep `ma-drawer__body` 作用域 CSS 含 `flex-shrink:0` | 规则存在（见 §2.3） | 半自动 |
| R10 | C10 + store | 节点挂 2 卡 | `DELETE /mapboards/:id/nodes/:nid`（默认 unbind） | `unbound=2,deleted=0`；卡片 `node_id=''`、库内 `anchor_mode='node'`、`map_geom` 保留（已覆盖 `TestDeleteNodeUnbindKeepsCards`） | 可自动 |
| R11 | store D0 + note | 有兜底节点 + 无兜底节点 | `DELETE /channels/:id` | 有兜底→转静态(`channel_id=''`,`static=1`)；无兜底→随通道删；挂无兜底节点的卡解绑（已覆盖 3 个用例） | 可自动 |
| R12 | store `noChannelSQL` | 无 | 验证 `channel_id` 落库为 NULL 而非空串（FK 强耦合） | NULL 写入成功；若改 DDL 为 `NOT NULL DEFAULT ''` 会整事务回滚（见 §5 + 风险） | 可自动（已覆盖 `TestDeleteChannelKeepsFallbackNodeAsStatic`） |
| R13 | mergeMapNodePatch | 节点有 name+兜底坐标 | `PUT nodes/:nid` 只带 `fallbackCrs` | name/坐标/通道保留；落库一致（已覆盖 `TestUpdateMapNodeIsPatchNotFullReplace`） | 可自动 |
| R14 | mergeMapNodePatch 限制 | 节点有兜底坐标 | `PUT nodes/:nid` 试图把 `fallbackLat/Lon` 置空 | **无手段表达"清空坐标"**：`nil`/空串=不改 → 坐标无法被清空（产品缺口，见 §7） | **测不了**（待确认） |
| R15 | bumpMapBoardRevision (H16) | 无 | 并发/过期 `revision` 调节点 CRUD | 旧 revision→40009；同事务内 revision 自增与节点变更原子（已覆盖 `TestMapNodeCRUD`/`TestNodeRevisionConflict`/`TestReplaceCardsAndUpdateDashboardAtomic`） | 可自动 |
| R16 | C7 + api | 卡片 | 连续 3 次 `PUT /geometry` | 不递增 `dashboards.revision`；永不返 40009（已覆盖 `TestGeometryEndpointDoesNotBumpRevision`） | 可自动 |
| R17 | api `updateMapGeometry` | 无 | `PUT /geometry` 发 `items:[]`；`items` 超 200；单条 `w=70`/`w=9999`；单条 `x:NaN` | 空→40001；超量→40001；尺寸越界→40001+detail；NaN→40001（**缺口**，应补） | 可自动（缺口） |
| R18 | validateAndEnrichNode | 无 | 建静态节点不带兜底坐标 | 40001 + `detail.field=fallbackLat,reason=required_without_channel`（已覆盖 `TestStaticNodeRequiresFallback`） | 可自动 |
| R19 | PositionPayloadTypes | 非 geo_pose 通道 | 节点绑 `scalar` 通道 | 40003（已覆盖 `TestNodeChannelPayloadWhitelist`） | 可自动 |
| R20 | kind 守卫 | grid 看板 | `GET /mapboards/:gridId` | 40003（已覆盖 `TestMapBoardKindGuard`） | 可自动 |
| R21 | D0-4 | 无 | 建满 20 个再建第 21 个 | 第 21 个失败（已覆盖 `TestMapNodeMaxPerBoard`） | 可自动 |
| R22 | H11 + api | 节点+卡片 | `PUT /mapboards/:id`/`PUT /dashboards/:id` 不带 `nodeId`/`cards` | nodeId/mapGeom/卡片不丢；不带 cards 不删卡；显式空数组才删（已覆盖 4 个用例） | 可自动 |
| R23 | store `geomVersion` | 无 | resize 卡片后 `geomVersion` 递增 | 响应式消费者读到新矩形，不弹回旧尺寸 | 可自动（store 单测） |
| R24 | useViewportCull (P-06) | 无 | `evaluate` 在边缘来回蹭；彻底出界等 grace；回视野立即恢复 | 隐藏带 hysteresis+grace(1000ms)、恢复立即；稳定态 `flips()=0` | 可自动（纯判定单测） |
| R25 | A10 + store | 无 | 节点先收坐标再断流；`setNodePosition` 已 `everReceived` | 断流后保留最后位置不跳兜底；`markNodeUninitialized` 仅从未收到时置灰 | 可自动（store 单测） |
| R26 | validateMapCardBinding `node_other_board` | 跨板 nodeId | 建卡引用别的看板的 node | 40001 + `reason=node_other_board`（**缺口**，应补） | 可自动（缺口） |
| R27 | ValidateMapNode CRS | 无 | `fallbackCrs:"MARS"` | 40001 + `detail.field=fallbackCrs,reason=unsupported`（**缺口**，应补） | 可自动（缺口） |
| R28 | ValidateMapNode 范围 | 无 | `fallbackLat:91` | 40001 + `detail.field=fallbackLat,reason=out_of_range`（**缺口**，应补） | 可自动（缺口） |
| R29 | ValidateMapNode 成对 | 无 | 只带 `fallbackLat` 不带 `fallbackLon` | 40001 + `reason=must_pair_with_lon`（**缺口**，应补） | 可自动（缺口） |
| R30 | schema `map_geom` CHECK | 无 | 直接写非法 `map_geom`（数组） | CHECK 拒绝（已覆盖 `TestMapGeomJSONCheck`） | 可自动 |
| R31 | schema `fallback_*` CHECK | 无 | 写 `fallback_lat=91` 或只写 lat | CHECK 拒绝（**缺口**，写库层用例） | 可自动（缺口） |
| R32 | 迁移链 v2→v3 | v2 库 | `Migrate` | 版本到 v4；旧看板 kind=grid；旧卡三列默认；升级前生成备份；不降级（已覆盖 `TestMigrateV2ToV3` 等） | 可自动 |
| R33 | useViewportCull 观测 P-06 | 真浏览器+实时 | 60s 内节点在边缘反复进出 | 切换次数=0 | 只能人工 |
| R34 | note/07 §3.8 视频死锁 | 视频卡 | 视频卡挂载即渲染（不等帧） | 不出现"未收到数据"死锁 | 半自动（WS 探针 + 日志） |
| R35 | 合规 C-05 | 无 | `grep -rn gcoord web/src` | 只命中 `utils/geo.ts`（转换唯一入口） | 半自动（静态 grep） |
| R36 | note/04 取点逆转换 | 无 | `fromGCJ02(lon,lat,'WGS-84')` | 取点 GCJ-02→WGS-84 不偏（**缺口**，应补 fromGCJ02 单测） | 可自动（缺口） |
| R37 | FindDirtyNodeRefs | 脏引用 | `cards.node_id` 指向不存在/跨板节点 | 自检报告 `node_missing`/`node_other_board`，**只报告不修**（已覆盖 `TestFindDirtyNodeRefs`） | 可自动 |
| R38 | store hydrate 守卫 | 无 | 切看板时 `openCardIds` 清空 | 不残留「幽灵卡」（**缺口**，store 单测） | 可自动（缺口） |
| R39 | api logo 安全 | 无 | 上传 svg/超大/类型不符/空/路径穿越 | 400 拒绝、不落盘、文件名由服务端定（已覆盖 `TestLogoUploadRejects*`） | 可自动 |
| R40 | runtime 信息泄露 | 无 | `GET /system/runtime` | 下发 version/commit/branding/weather；**不含 dataDir**（已覆盖 `TestRuntimeIncludesVersionAndCommit`） | 可自动 |
| R41 | 天气 ok=false | 无 | 关天气 / 坐标缺失 / provider 非法 / 第三方不可达 | 各 reason 正确（已覆盖 `TestWeather*`） | 可自动 |
| R42 | note/06 §6.2 WS 探针 | 运行实例 | Node WS 订阅 geo_pose 通道 | 收到 `frames`、坐标合法 → 节点会动（无浏览器验证后端帧下发） | 半自动 |
| R43 | note/06 §6.3 日志量化 | 运行实例 | 配对 WS 建立/关闭日志算存活分布 | 不断连 / 稳定存活（排障用） | 半自动 |
| R44 | projector.ts 2D 前提 | 无 | 启 pitch/rotate 时投影 | 静默失效（无报错，仅浮层偏移）—— 当前无能力，仅文档红线 | **测不了**（风险） |
| R45 | 前端静态检查 / 死代码 | 无 | `grep -rn 'setMapStyle' web/src` 等 | 确认能力真被调用，非死代码（note/06 §6.5） | 半自动 |
| R46 | cascade=delete | 节点挂卡 | `DELETE /mapboards/:id/nodes/:nid?cascade=delete` | 卡片一并删（已覆盖 `TestDeleteNodeCascade`） | 可自动 |
| R47 | 删看板级联 | 看板含节点 | `DELETE /mapboards/:id` | 节点随之清（已覆盖 `TestDeleteDashboardCascadesNodes`） | 可自动 |

**回归用例合计：47 条**。其中**可自动 34 条**（含已覆盖与标注"缺口"待补）、**半自动 9 条**（R03/R05/R07/R09/R34/R35/R42/R43/R45）、**只能人工 2 条**（R04/R33）、**测不了 2 条**（R14 清空坐标、R44 投影器 2D 前提，见 §6 风险）。

---

## 4. 数据层专项

### 4.1 迁移测试方法（在真库副本上跑）

绝不碰真库。流程：

```bash
export PATH="$PATH:/usr/local/go/bin"
export GOTMPDIR="$HOME/.cache/monitorall/tmp"
mkdir -p "$GOTMPDIR" /tmp/mbtest
cp /home/jack/monitorAll/data/monitorall.db /tmp/mbtest/before.db
cd /home/jack/monitorAll/server
go test ./internal/store/ -run 'TestMigrateV2ToV3|TestMigrationChainComplete|TestMigrateRejectsDowngrade' -count=1 -v
```

核对迁移前后：用 `sqlite3` 或 Go 小脚本读 `meta.schema_version`（前=2/3，后=v4）、各表行数（`dashboards`/`cards`/`map_nodes`/`channels` 不减、`map_nodes` 新建）、`sqlite_master` 中 `map_nodes`/`idx_map_nodes_board` 等存在。已盖 `TestMigrateV2ToV3`。

### 4.2 外键 `ON DELETE` 语义

- `map_nodes.board_id` → `dashboards(id) ON DELETE CASCADE`：删看板节点随删（R47，已盖）。
- `map_nodes.channel_id` → `channels(id) ON DELETE SET NULL`：**删通道时节点 `channel_id` 置 NULL**（不是删节点）。但注意真实路径里 `DetachMapNodesByChannel` 在事务内先按"有无兜底"决定转静态或删节点，FK 的 SET NULL 是兜底（R11/R12）。
- **关键不变量（R12）**：`noChannelSQL = NULL`。若把 DDL 改成 `channel_id TEXT NOT NULL DEFAULT ''` 并去掉外键，删通道事务会因 NOT NULL 约束回滚——这是曾实测的坑。回归用例 `TestDeleteChannelKeepsFallbackNodeAsStatic` 锁死"NULL 可写"。
- 卡片外键：节点删除默认只清 `node_id`（解绑保留），`cascade=delete` 才删卡（R10/R46）。
- **验证外键恒开**（H10）：`PRAGMA foreign_keys` 在 WAL=true/false 下都须为 1（已盖 `TestForeignKeysEnabledWithoutWAL`）。

### 4.3 `map_nodes` 的 CHECK 约束各写一条

```sql
-- 在临时库上直接验证（不碰真库）
-- ① 经纬度成对
INSERT INTO map_nodes(id,board_id,name,fallback_lat,fallback_lon)
 VALUES('n1','b1','x',39.9,NULL);            -- 应被 CHECK ((lat IS NULL)=(lon IS NULL)) 拒绝
-- ② 范围
INSERT INTO map_nodes(id,board_id,name,fallback_lat,fallback_lon)
 VALUES('n2','b1','x',91,116.4);             -- 应被 fallback_lat BETWEEN -90 AND 90 拒绝
INSERT INTO map_nodes(...) VALUES('n3','b1','x',39.9,200); -- lon 越界拒绝
-- ③ CRS 白名单
INSERT INTO map_nodes(...) VALUES('n4','b1','x',39.9,116.4,fallback_crs='MARS'); -- 拒绝
-- ④ name 非空
INSERT INTO map_nodes(id,board_id,name) VALUES('n5','b1','   '); -- CHECK(length(trim(name))>0) 拒绝
```

后端经 `ValidateMapNode` 已在 HTTP 层拦（R27/R28/R29 缺口），但**库层 CHECK 是最后一道保险**——即使绕过 API 直接写也应拒绝。建议补一条 store 层用例（R31）。

---

## 5. 验收标准（Gate）

### P0（阻断发布，任一不过不准发）

- 节点 CRUD + 乐观锁：过期 revision 必 40009、同事务原子（R15）。
- 补丁语义：局部更新不丢其它字段（R13）；但 R14「清空坐标」属已知缺口，不阻断（记风险）。
- 删节点默认解绑保留卡片、cascade 才删（R10/R46）。
- 删通道节点处置正确：有兜底转静态、无兜底删、挂卡解绑（R11）。
- 几何端点不递增 revision、不返 40009、参数校验（R16/R17，R17 补齐后端用例）。
- 位置通道白名单仅 geo_pose（R19）；地图看板禁 map 渲染器（R06）。
- 迁移 v2→v3 成功且升级前备份、不降级（R32）。
- 外键恒开、CHECK 拦非法坐标/CRS（R12/R31）。
- 全量保存不丢 nodeId/mapGeom/卡片（R22）。
- 全局设置：Logo 上传拒绝 svg/超大/穿越、runtime 不含 dataDir、天气 ok=false 各 reason（R39/R40/R41）。
- 构建产物核对通过：`__maMap`/`fallbackLat`/`channelMissing`/`ma-drawer__body flex-shrink:0` 均在 dist 中（§2.3）。

### P1（发布前应过，可带已知限制发）

- 视口剔除稳定态 flips=0（R24，纯逻辑已测；真机观测 R33 为人工）。
- 节点 stale 不瞬移（R25）。
- 坐标系转换单测覆盖 toGCJ02/fromGCJ02（R02/R36）。
- 投影器数学往返（R01）。
- 脏引用自检只报告不修（R37）。

### P2（跟踪项，不阻断）

- 暗色/卫星真实视觉（R04，只能人工）。
- 无 Key 降级视觉（R05，半自动）。
- WS 实时帧下发（R42，半自动探针）。
- hydrate 守卫 / 幽灵卡（R38，缺口单测）。

---

## 6. 已知风险与未覆盖项（诚实清单）

1. **R14「清空坐标表达不了」——测不了，产品缺口**：`mergeMapNodePatch` 把 `nil`/空串当"不改动"，所以 HTTP 层**没有任何字段能表达"把 fallbackLat/Lon 清空"**。用户想去掉兜底坐标只能删节点重建或改绑通道。验收时若产品要求"可清空"，需先改 `MapNodeReq`（例如引入 `clearFallback` 显式标志）再测。当前判为**待确认**。
2. **R44 投影器 2D 前提——测不了**：`projector.ts` 数学只对 2D 俯视成立；一旦启用 pitch/rotate/3D 即静默失效（不报错、仅浮层偏移）。当前无该能力，仅作架构红线；若未来加视角能力，必须同步改投影器并补测。
3. **`channelMissing=true` 运行路径难触发**：因为 `map_nodes.channel_id` 有 `ON DELETE SET NULL` 外键，通道被删后节点 `channel_id` 自动变 NULL（再由 `DetachMapNodesByChannel` 处理），所以 `getMapBoard` 里 `channelMissing=true`（指向"存在却查不到的通道"）在 FK 正常时几乎不可达；它只是脏数据的防御分支。**待确认**：是否值得构造（绕过 FK 直写）来覆盖——性价比低，建议保留为人工/代码审查项。
4. **真实高德视觉（暗色/卫星/WebGL/Key 配额）只能人工**：本环境无浏览器无 Key，无法自动化（R04/R05/R33/R45）。人工走查清单见 §2.4。
5. **性能（20 节点 × 10Hz）不纳入本次**：属性能验收，需真浏览器+真数据，另行排期。
6. **`DetachMapNodesByChannel` 与 `noChannelSQL` 强耦合 DDL**：改 `map_nodes.channel_id` 的 DDL（如改为 NOT NULL DEFAULT ''）会使删通道事务回滚。已用 `TestDeleteChannelKeepsFallbackNodeAsStatic` 锁死当前形态，但**属于"改 DDL 必同批验证"的活文档风险**。

---

## 7. 全局设置（品牌 + 天气）接口冒烟清单（curl 级，可直接跑）

相邻 surface，紧凑覆盖，不展开成完整方案。以下命令**真能跑**；用真库副本起临时实例，绝不直接动真库。

```bash
export PATH="$PATH:/usr/local/go/bin"
export GOTMPDIR="$HOME/.cache/monitorall/tmp"
mkdir -p "$GOTMPDIR" /tmp/mbtest
# 真库副本（只读验证用，禁止写真库）
cp /home/jack/monitorAll/data/monitorall.db /tmp/mbtest/monitorall.db
cd /home/jack/monitorAll/server
# 用已构建二进制起临时实例（或：go build -o /tmp/mbtest/monitorall ./cmd/monitorall）
MONITORALL_SERVER_HTTPADDR=:9080 \
MONITORALL_STORE_DSN=/tmp/mbtest/monitorall.db \
MONITORALL_SERVER_DATADIR=/tmp/mbtest \
./bin/monitorall --config server/config.yaml >/tmp/mbtest/srv.log 2>&1 &
SRV=$!
sleep 2
BASE=http://127.0.0.1:9080/api/v1

# 7.1 GET /settings 默认（DB 空时回落 config/默认）
curl -s $BASE/settings | python3 -m json.tool

# 7.2 PUT /settings 校验分支（应 400 且 detail 指明字段）
echo "--- 标题超长(>40) ---"
curl -s -o /tmp/r -w '%{http_code}\n' -X PUT $BASE/settings -H 'Content-Type: application/json' \
  -d '{"branding":{"title":"'"$(printf '名%.0s' {1..50})"'"},"weather":{"enabled":false}}'
grep -o 'branding.title' /tmp/r
echo "--- 服务商白名单(amap 不在列) ---"
curl -s -o /tmp/r -w '%{http_code}\n' -X PUT $BASE/settings -H 'Content-Type: application/json' \
  -d '{"branding":{"title":"x"},"weather":{"enabled":true,"provider":"amap","lat":22.5,"lon":113.9}}'
grep -o 'weather.provider' /tmp/r
echo "--- 开天气但坐标缺失(0,0) ---"
curl -s -o /tmp/r -w '%{http_code}\n' -X PUT $BASE/settings -H 'Content-Type: application/json' \
  -d '{"branding":{"title":"x"},"weather":{"enabled":true,"provider":"open-meteo","lat":0,"lon":0}}'
grep -o '开启天气前请先填写坐标' /tmp/r
echo "--- 纬度越界 91 ---"
curl -s -o /tmp/r -w '%{http_code}\n' -X PUT $BASE/settings -H 'Content-Type: application/json' \
  -d '{"branding":{"title":"x"},"weather":{"enabled":true,"provider":"open-meteo","lat":91,"lon":113.9}}'
grep -o 'weather.lat' /tmp/r
echo "--- Logo 用 javascript: 协议 ---"
curl -s -o /tmp/r -w '%{http_code}\n' -X PUT $BASE/settings -H 'Content-Type: application/json' \
  -d '{"branding":{"title":"x","logoUrl":"javascript:alert(1)"},"weather":{"enabled":false}}'
grep -o 'branding.logoUrl' /tmp/r

# 7.3 合法 PUT 再 GET 回读
curl -s -X PUT $BASE/settings -H 'Content-Type: application/json' -d '{
  "branding":{"title":"机器人监控","subtitle":"四足集群","logoUrl":""},
  "weather":{"enabled":true,"provider":"open-meteo","lat":22.53,"lon":113.93,"label":"深圳南山","refreshMin":15}
}' >/dev/null
curl -s $BASE/settings | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print("title=",d["branding"]["title"],"weather.enabled=",d["weather"]["enabled"])'

# 7.4 GET /weather 各 ok:false reason
echo "--- 关天气 -> reason=disabled ---"
curl -s -X PUT $BASE/settings -H 'Content-Type: application/json' -d '{"branding":{"title":"x"},"weather":{"enabled":false}}' >/dev/null
curl -s $BASE/weather | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print("ok=",d["ok"],"reason=",d["reason"])'
# 开天气但坐标 0,0（yaml 直产场景）-> reason=coords_invalid
curl -s -X PUT $BASE/settings -H 'Content-Type: application/json' -d '{"branding":{"title":"x"},"weather":{"enabled":true,"provider":"open-meteo","lat":0,"lon":0}}' >/dev/null
curl -s $BASE/weather | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print("ok=",d["ok"],"reason=",d["reason"])'

# 7.5 Logo 上传：类型/大小/路径穿越拒绝（curl -F）
cd /home/jack/monitorAll
printf '\x89PNG\r\n\x1a\n' > /tmp/mbtest/ok.png
printf '<svg><script>alert(1)</script></svg>' > /tmp/mbtest/bad.svg
echo "--- 合法 png 应 200 ---"
curl -s -o /dev/null -w '%{http_code}\n' -F "file=@/tmp/mbtest/ok.png;type=image/png" $BASE/settings/logo
echo "--- svg(XSS) 应 400 ---"
curl -s -o /dev/null -w '%{http_code}\n' -F "file=@/tmp/mbtest/bad.svg;type=image/svg+xml" $BASE/settings/logo
echo "--- 文件名 ../ 穿越：合法 png 只是名可疑，应正常接受且不写到 dataDir 外 ---"
curl -s -o /dev/null -w '%{http_code}\n' -F "file=@/tmp/mbtest/ok.png;filename=../../evil.png" $BASE/settings/logo
ls /tmp/mbtest/evil.png 2>/dev/null && echo "BUG: 路径穿越!" || echo "OK: 未穿越"

# 7.6 GET /system/runtime 必须不含 dataDir
curl -s $BASE/system/runtime | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print("version=",d.get("version"));print("含dataDir=", "dataDir" in d)'

kill $SRV 2>/dev/null
```

判定：7.2 各分支均 400 且 grep 到对应字段名；7.4 关天气→`disabled`、坐标缺失→`coords_invalid`；7.5 png→200、svg→400、穿越→未写到 dataDir 外；7.6 `dataDir` 不在响应中 → 全局设置冒烟通过。

---

## 8. 文末：现有测试覆盖与建议补齐的缺口（给研发）

**已覆盖（勿重复）**：`store/mapnode_test.go`（CRUD/上限/解绑保留/级联/删通道处置/几何/脏引用/级联看板/网格卡 JSON 不变/外键 NULL）、`api/board_v3_test.go`（H11 保留/node 绑定保留/kind 守卫/C11 禁 map 渲染器/白名单/静态节点需兜底/几何不 bump revision/节点 revision 冲突/补丁语义/不带 cards 不删卡/空数组删卡）、`store/schema_v3_test.go`（迁移链/备份/FK/CHECK）、`api/settings_test.go`（品牌/天气/Logo/runtime 全覆盖）。

**建议补齐的缺口（本文档标"缺口"的用例）**：
1. `model` 层单测 `ValidateMapNode` / `ValidateGeometry` / `ValidateNodeChannel`：CRS 非法、纬度越界、经纬度不成对、name 空/超长、yawUnit 非法、几何尺寸/偏移/NaN（R27/R28/R29 + 几何校验）。
2. `api` 层 `PUT /geometry` 参数校验：空 items、超 200、单条 w/h 越界、NaN/Inf offset（R17）。
3. `api` 层卡片跨板 node 引用拒绝（R26）。
4. `store` 层 `map_nodes` CHECK 直写用例：经纬度成对、范围、CRS 白名单、name 非空（R31）。
5. `store` 层 `fromGCJ02` 逆转换（已随 geo.ts 前端单测，后端无）；前端补 `fromGCJ02` 单测（R36）。
6. `store` 层 hydrate 守卫/幽灵卡（R38）。
7. 前端 `useViewportCull.evaluate` 纯判定单测（R24）；`useNodePosition` 状态机（`everReceived` 不回退兜底，R25）——需对 `window` 垫片。

---

*文档结束。所有命令含 `PATH`/`GOTMPDIR` 前置，且任何写操作都基于真库副本。*
